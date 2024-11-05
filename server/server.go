package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi"
	"github.com/huaweicloud/huaweicloud-sdk-go-obs/obs"
	"github.com/metalogical/BigFiles/auth"
	"github.com/metalogical/BigFiles/batch"
)

var ObsPutLimit int = 5*int(math.Pow10(9)) - 1 // 5GB - 1
var oidRegexp = regexp.MustCompile("^[a-f0-9]{64}$")

type Options struct {
	// required
	Endpoint     string
	NoSSL        bool
	Bucket       string
	CdnDomain    string
	S3Accelerate bool

	// minio auth (required)
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string

	// optional
	TTL    time.Duration // defaults to 1 hour
	Prefix string

	IsAuthorized func(auth.UserInRepo) error
}

func (o Options) imputeFromEnv() (Options, error) {
	if o.Endpoint == "" {
		region := os.Getenv("OBS_REGION")
		if region == "" {
			return o, errors.New("endpoint required")
		}
		o.Endpoint = region
	}
	if o.AccessKeyID == "" {
		o.AccessKeyID = os.Getenv("OBS_ACCESS_KEY_ID")
		if o.AccessKeyID == "" {
			return o, fmt.Errorf("OBS access key ID required for %s", o.Endpoint)
		}
		o.SecretAccessKey = os.Getenv("OBS_SECRET_ACCESS_KEY")
		if o.SecretAccessKey == "" {
			return o, fmt.Errorf("OBS secret access key required for %s", o.Endpoint)
		}
		o.SessionToken = os.Getenv("OBS_SESSION_TOKEN")
	}
	if o.Bucket == "" {
		return o, fmt.Errorf("bucket required")
	}
	if o.TTL == 0 {
		o.TTL = time.Hour
	}

	return o, nil
}

func New(o Options) (http.Handler, error) {
	o, err := o.imputeFromEnv()
	if err != nil {
		return nil, err
	}

	client, err := obs.New(o.AccessKeyID, o.SecretAccessKey, o.Endpoint, obs.WithSignature(obs.SignatureObs))
	if err != nil {
		fmt.Printf("Create obsClient error, errMsg: %s", err.Error())
	}

	s := &server{
		client:       client,
		bucket:       o.Bucket,
		prefix:       o.Prefix,
		ttl:          o.TTL,
		cdnDomain:    o.CdnDomain,
		isAuthorized: o.IsAuthorized,
	}
	r := chi.NewRouter()

	r.Get("/", s.healthCheck)
	r.Post("/{owner}/{repo}/objects/batch", s.handleBatch)

	return r, nil
}

type server struct {
	ttl       time.Duration
	client    *obs.ObsClient
	bucket    string
	prefix    string
	cdnDomain string

	isAuthorized func(auth.UserInRepo) error
}

func (s *server) key(oid string) string {
	return s.prefix + oid
}

func (s *server) handleBatch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	var req batch.Request
	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		w.WriteHeader(404)
		must(json.NewEncoder(w).Encode(batch.ErrorResponse{
			Message: "could not parse request",
			DocURL:  "https://github.com/git-lfs/git-lfs/blob/v2.12.0/docs/api/batch.md#requests",
		}))
		return
	}

	var userInRepo auth.UserInRepo
	userInRepo.Operation = req.Operation
	userInRepo.Owner = chi.URLParam(r, "owner")
	userInRepo.Repo = chi.URLParam(r, "repo")
	if err = auth.CheckRepoOwner(userInRepo); req.Operation == "upload" || err != nil {
		if username, password, ok := r.BasicAuth(); ok {
			userInRepo.Username = username
			userInRepo.Password = password
			err = s.isAuthorized(userInRepo)
		} else {
			err = errors.New("unauthorized: cannot get password")
		}
		if err != nil {
			v := err.Error()
			switch {
			case strings.HasPrefix(v, "unauthorized") || strings.HasPrefix(v, "not_found"):
				w.WriteHeader(401)
			case strings.HasPrefix(v, "forbidden"):
				w.WriteHeader(403)
			default:
				w.WriteHeader(500)
			}
			w.Header().Set("LFS-Authenticate", `Basic realm="Git LFS"`)
			must(json.NewEncoder(w).Encode(batch.ErrorResponse{
				Message: v,
			}))
			return
		}
	}

	var resp batch.Response

	for _, in := range req.Objects {
		resp.Objects = append(resp.Objects, batch.Object{
			OID:  in.OID,
			Size: in.Size,
		})
		out := &resp.Objects[len(resp.Objects)-1]

		if !oidRegexp.MatchString(in.OID) {
			out.Error = &batch.ObjectError{
				Code:    422,
				Message: "oid must be a SHA-256 hash in lower case hexadecimal",
			}
			continue
		}

		switch req.Operation {
		case "download":
			getObjectMetadataInput := &obs.GetObjectMetadataInput{
				Bucket: s.bucket,
				Key:    s.key(in.OID),
			}
			if metadata, err := s.client.GetObjectMetadata(getObjectMetadataInput); err != nil {
				out.Error = &batch.ObjectError{
					Code:    404,
					Message: err.Error(),
				}
				continue
			} else if in.Size != int(metadata.ContentLength) {
				out.Error = &batch.ObjectError{
					Code:    422,
					Message: "found object with wrong size",
				}
			}
			getObjectInput := &obs.CreateSignedUrlInput{}
			getObjectInput.Method = obs.HttpMethodGet
			getObjectInput.Bucket = s.bucket
			getObjectInput.Key = s.key(in.OID)
			getObjectInput.Expires = int(s.ttl / time.Second)
			getObjectInput.Headers = map[string]string{"Content-Type": "application/octet-stream"}
			// 生成下载对象的带授权信息的URL
			getObjectOutput, err := s.client.CreateSignedUrl(getObjectInput)
			if err != nil {
				panic(err)
			}
			v, _ := url.Parse(getObjectOutput.SignedUrl)
			// if err == nil {
			// 	v.Host = s.cdnDomain
			// 	v.Scheme = "https"
			// } else {
			// 	logrus.Infof("%s cannot be parsed", getObjectOutput.SignedUrl)
			// 	panic(err)
			// }

			out.Actions = &batch.Actions{
				Download: &batch.Action{
					HRef:      v.String(),
					Header:    getObjectInput.Headers,
					ExpiresIn: int(s.ttl / time.Second),
				},
			}

		case "upload":
			if out.Size > ObsPutLimit {
				out.Error = &batch.ObjectError{
					Code:    422,
					Message: "cannot upload objects larger than 5GB to S3 via LFS basic transfer adapter",
				}
				continue
			}

			putObjectInput := &obs.CreateSignedUrlInput{}
			putObjectInput.Method = obs.HttpMethodPut
			putObjectInput.Bucket = s.bucket
			putObjectInput.Key = s.key(in.OID)
			putObjectInput.Expires = int(s.ttl / time.Second)
			putObjectInput.Headers = map[string]string{"Content-Type": "application/octet-stream"}
			putObjectOutput, err := s.client.CreateSignedUrl(putObjectInput)
			if err != nil {
				panic(err)
			}

			out.Actions = &batch.Actions{
				Upload: &batch.Action{
					HRef:      putObjectOutput.SignedUrl,
					Header:    putObjectInput.Headers,
					ExpiresIn: int(s.ttl / time.Second),
				},
			}
		}
	}
	must(json.NewEncoder(w).Encode(resp))
}

func (s *server) healthCheck(w http.ResponseWriter, r *http.Request) {
	response := batch.SuccessResponse{
		Message: "Success",
		Data:    "healthCheck success",
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	must(json.NewEncoder(w).Encode(response))
}

// --

func must(err error) {
	if err != nil {
		panic(err)
	}
}
