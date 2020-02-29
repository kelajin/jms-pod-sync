package jumpserver

import (
	"context"
	"fmt"

	"github.com/antihax/optional"
	jms "github.com/kelajin/jumpserver-go-sdk"
)

// JS is jumpserver
type JS struct {
	host     string
	username string
	password string
	client   *jms.APIClient
	ctx      *context.Context
	cfg      *jms.Configuration
}

//NewClient initialize a new jumpserver client
func NewClient(host, username, password string) (*JS, error) {
	j := &JS{}
	if j.client == nil {
		j.host = host
		j.username = username
		j.password = password
		j.cfg = jms.NewConfiguration()
		j.cfg.BasePath = host
		j.client = jms.NewAPIClient(j.cfg)
	}
	token, _, err := j.client.AuthenticationAuthApi.AuthenticationAuthCreate(nil, jms.BearerToken{
		Username: username,
		Password: password,
	})
	tmp := context.WithValue(context.Background(), jms.ContextAPIKey, jms.APIKey{
		Key:    token.Token,
		Prefix: token.Keyword,
	})
	j.ctx = &tmp
	if err != nil {
		return nil, err
	}
	return j, nil
}

//GetAccessToken get an access token from jms
func (j *JS) GetAccessToken() (string, error) {
	token, _, err := j.client.AuthenticationAuthApi.AuthenticationAuthCreate(nil, jms.BearerToken{
		Username: j.username,
		Password: j.password,
	})
	if err != nil {
		return "", err
	}
	return token.Token, nil
}

func (j *JS) refreshAccessToken() error {
	token, _, err := j.client.AuthenticationAuthApi.AuthenticationAuthCreate(nil, jms.BearerToken{
		Username: j.username,
		Password: j.password,
	})
	if err != nil {
		return err
	}
	tmp := context.WithValue(context.Background(), jms.ContextAPIKey, jms.APIKey{
		Key:    token.Token,
		Prefix: token.Keyword,
	})
	j.ctx = &tmp
	return nil
}

//ListUsers list users
func (j *JS) ListUsers(offset, limit int32) ([]jms.User, error) {
	defer j.refreshAccessToken()
	res, _, err := j.client.UsersUsersApi.UsersUsersList(*j.ctx, &jms.UsersUsersListOpts{
		Offset: optional.NewInt32(offset),
		Limit:  optional.NewInt32(limit),
	})
	if err != nil {
		return nil, err
	}
	return res.Results, nil
}

//AddAsset to jumpserver
func (j *JS) AddAsset(ip, hostname, platform string, port int32) (string, error) {
	asset, _, err := j.client.AssetsAssetsApi.AssetsAssetsCreate(*j.ctx, jms.Asset{
		Ip:       ip,
		Hostname: hostname,
		Port:     port,
		Platform: platform,
	})
	if err != nil {
		return "", err
	}
	return asset.Id, nil
}

//HaveAsset return if asset already exists
func (j *JS) HaveAsset(hostname string) bool {
	_, err := j.GetAssetByHostname(hostname)
	return err == nil
}

//HaveAssetUser return if asset already exists in user
func (j *JS) HaveAssetUser(username, assetID string) bool {
	res, _, err := j.client.AssetsAssetUsersInfoApi.AssetsAssetUsersInfoList(*j.ctx, &jms.AssetsAssetUsersInfoListOpts{
		Limit:  optional.NewInt32(65535),
		Offset: optional.NewInt32(0),
	})
	if err != nil {
		return false
	}
	for _, aue := range res.Results {
		if aue.Asset == assetID {
			return true
		}
	}
	return false
}

//AddAssetToUser to jumpserver
func (j *JS) AddAssetToUser(username string, assetID string) error {
	_, _, err := j.client.AssetsAssetUsersApi.AssetsAssetUsersCreate(*j.ctx, jms.AssetUser{
		Username: username,
		Asset:    assetID,
	})
	if err != nil {
		return err
	}
	return nil
}

//GetAssetByHostname from jumpserver
func (j *JS) GetAssetByHostname(hostname string) (jms.Asset, error) {
	asset, _, err := j.client.AssetsAssetsApi.AssetsAssetsList(*j.ctx, &jms.AssetsAssetsListOpts{
		Hostname: optional.NewString(hostname),
	})
	if err != nil {
		return jms.Asset{}, err
	}
	res := asset.Results
	if len(res) > 0 {
		return res[0], nil
	}
	return jms.Asset{}, fmt.Errorf("can not found asset which hostname is " + hostname)
}

//DelAsset from jumpserver
func (j *JS) DelAsset(hostname string) error {
	asset, err := j.GetAssetByHostname(hostname)
	if err != nil {
		return err
	}
	assetID := asset.Id
	_, err = j.client.AssetsAssetsApi.AssetsAssetsDelete(*j.ctx, assetID)
	if err != nil {
		return err
	}
	return nil
}
