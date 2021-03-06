package main

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"

	"github.com/doomsday-project/doomsday/storage/uaa"
)

type client struct {
	URL               string
	Username          string
	Password          string
	AccessToken       string
	RefreshToken      string
	SkipSSLValidation bool
	isBasic           bool
	cache             map[string]string
}

type boshInfo struct {
	Auth struct {
		Type    string `json:"type"`
		Options struct {
			URL string `json:"url"`
		} `json:"options"`
	} `json:"user_authentication"`
}

var schemeRegex = regexp.MustCompile("^(http|https)://")

func (c client) path(path string) string {
	uStr := c.URL
	if !schemeRegex.MatchString(uStr) {
		uStr = "https://" + uStr
	}

	u, err := url.Parse(uStr)
	if err != nil {
		return c.URL + path
	}

	if u.Port() == "" {
		u.Host = u.Host + ":25555"
	}

	u.Path = path
	u.RawPath = path
	return u.String()
}

func (c client) basicAuthHeader() string {
	return fmt.Sprintf("Basic %s",
		base64.StdEncoding.EncodeToString(
			[]byte(fmt.Sprintf("%s:%s", c.Username, c.Password)),
		),
	)
}

func (c client) accessTokenHeader() string {
	return fmt.Sprintf("Bearer %s", c.AccessToken)
}

func (c *client) fetchAuthHeader() (string, error) {
	if c.AccessToken != "" {
		return c.accessTokenHeader(), nil
	}

	if c.isBasic {
		c.basicAuthHeader()
	}

	if c.Username == "" && c.Password == "" && c.RefreshToken == "" {
		return "", fmt.Errorf("No authorization options. Need to log in")
	}

	//Check out /info for the type of auth
	req, err := http.NewRequest("GET", c.path("/info"), nil)
	if err != nil {
		return "", err
	}

	info := boshInfo{}
	err = c.Do(req, "/info", &info)
	if err != nil {
		return "", err
	}

	header := ""
	switch info.Auth.Type {
	case "basic":
		c.isBasic = true
		header = c.basicAuthHeader()
	case "uaa":
		uaac := uaa.Client{
			URL:               info.Auth.Options.URL,
			SkipTLSValidation: true,
		}

		var authResp *uaa.AuthResponse
		if c.RefreshToken != "" {
			log.Write("Performing refresh token grant UAA auth")
			authResp, err = uaac.Refresh("bosh_cli", "", c.RefreshToken)
		} else {
			log.Write("Performing password grant UAA auth")
			log.Write("with username `%s' and password `%s'", c.Username, c.Password)
			authResp, err = uaac.Password("bosh_cli", "", c.Username, c.Password)
		}

		if err == nil {
			c.AccessToken = authResp.AccessToken
			header = c.accessTokenHeader()
		}

	default:
		err = fmt.Errorf("Unknown auth type: `%s'", info.Auth.Type)
	}

	return header, err
}

func (c *client) Get(path string, output interface{}) error {
	cacheBody, cacheHit := c.cache[path]
	if cacheHit {
		log.Write("http cache hit: %s", path)
		err := json.NewDecoder(strings.NewReader(cacheBody)).Decode(output)
		return err
	}
	log.Write("http cache miss: %s", path)
	authHeader, err := c.fetchAuthHeader()
	if err != nil {
		return err
	}

	req, err := http.NewRequest("GET", c.path(path), nil)
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", authHeader)

	return c.Do(req, path, output)
}

func (c *client) Do(req *http.Request, path string, output interface{}) error {
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: c.SkipSSLValidation,
			},
		},
	}

	dump, err := httputil.DumpRequestOut(req, true)
	if err == nil {
		log.Write("%s", string(dump))
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	dump, err = httputil.DumpResponse(resp, true)
	if err == nil {
		log.Write("%s", string(dump))
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("Non-2xx response code")
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	log.Write("Inserting to cache: %s", path)
	c.cache[path] = string(bodyBytes)

	if output != nil {
		err := json.Unmarshal(bodyBytes, output)
		if err != nil {
			return err
		}
	}

	return nil
}
