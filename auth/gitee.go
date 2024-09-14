package auth

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/metalogical/BigFiles/config"
)

var (
	client_id     string
	client_secret string
)

type giteeUser struct {
	Login string `json:"login"`
	// Permission string `json:"permission"`
}

type AccessToken struct {
	Token string `json:"access_token"`
}

func Init(cfg *config.Config) error {
	client_id = cfg.ClientId
	if client_id == "" {
		client_id = os.Getenv("CLIENT_ID")
		if client_id == "" {
			return errors.New("client id required")
		}
	}
	client_secret = cfg.ClientSecret
	if client_secret == "" {
		client_secret = os.Getenv("CLIENT_SECRET")
		if client_secret == "" {
			return errors.New("client secret required")
		}
	}

	return nil
}

func GiteeAuth() func(string, string) error {
	return func(username, password string) error {
		token, err := getToken(username, password)
		if err != nil {
			return err
		}

		return verifyUser(username, token)
	}
}

// getToken gets access_token by username and password
func getToken(username, password string) (string, error) {
	form := url.Values{}
	form.Add("scope", "user_info")
	form.Add("grant_type", "password")
	form.Add("username", username)
	form.Add("password", password)
	form.Add("client_id", client_id)
	form.Add("client_secret", client_secret)

	path := "https://gitee.com/oauth/token"
	response, err := http.Post(path, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		panic(err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return "", errors.New("invalid credentials")
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		panic(err)
	}
	var accessToken AccessToken
	err = json.Unmarshal(body, &accessToken)
	if err != nil {
		panic(err)
	}
	return accessToken.Token, nil
}

// verifyUser verifies user info by access_token
func verifyUser(username, token string) error {
	path := "https://gitee.com/api/v5/user?access_token=" + token
	req, err := http.NewRequest("GET", path, nil)
	if err != nil {
		panic(err)
	}
	req.Header.Set("Content-Type", "application/json;charset=UTF-8")

	response, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("invalid credentials")
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		panic(err)
	}
	var giteeUser giteeUser
	err = json.Unmarshal(body, &giteeUser)
	if err != nil {
		panic(err)
	}
	if giteeUser.Login == username {
		return nil
	} else {
		return errors.New("username does not match")
	}
}
