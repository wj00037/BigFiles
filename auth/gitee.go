package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

var uploadPermissions = []string{"admin", "developer"}
var downloadPermissions = []string{"admin", "developer", "read"}

type giteeUser struct {
	Login      string `json:"login"`
	Permission string `json:"permission"`
}

type UserInRepo struct {
	Repo      string
	Owner     string
	Token     string
	Username  string
	Password  string
	Operation string
}

type AccessToken struct {
	Token string `json:"access_token"`
}

func GiteeAuth() func(UserInRepo) error {
	return func(userInRepo UserInRepo) error {
		token, err := getToken(userInRepo.Username, userInRepo.Password)
		if err != nil {
			userInRepo.Token = userInRepo.Password
		} else {
			userInRepo.Token = token
		}

		return verifyUser(userInRepo)
	}
}

// getToken gets access_token by username and password
func getToken(username, password string) (string, error) {
	clientId := os.Getenv("CLIENT_ID")
	clientSecret := os.Getenv("CLIENT_SECRET")
	form := url.Values{}
	form.Add("scope", "user_info projects")
	form.Add("grant_type", "password")
	form.Add("username", username)
	form.Add("password", password)
	form.Add("client_id", clientId)
	form.Add("client_secret", clientSecret)

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

// verifyUser verifies user permission in repo by access_token 1231
func verifyUser(userInRepo UserInRepo) error {
	path := fmt.Sprintf(
		"https://gitee.com/api/v5/repos/%s/%s/collaborators/%s/permission?access_token=%s",
		userInRepo.Owner,
		userInRepo.Repo,
		userInRepo.Username,
		userInRepo.Token,
	)
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

	if giteeUser.Login != userInRepo.Username {
		return errors.New("username does not match")
	}
	if userInRepo.Operation == "upload" {
		for _, v := range uploadPermissions {
			if giteeUser.Permission == v {
				return nil
			}
		}
		return errors.New("user has no permission uploading to the repository")
	} else if userInRepo.Operation == "download" {
		for _, v := range downloadPermissions {
			if giteeUser.Permission == v {
				return nil
			}
		}
		return errors.New("user has no permission downloading in the repository")
	} else {
		return errors.New("unknow operation")
	}
}
