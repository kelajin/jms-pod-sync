package jumpserver

import (
	"context"
	"fmt"

	"github.com/antihax/optional"
	jms "github.com/kelajin/jumpserver-client-go"
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

//ListAssets list Assets
func (j *JS) ListAssets(offset, limit int32) ([]jms.Asset, error) {
	defer j.refreshAccessToken()
	res, _, err := j.client.AssetsAssetsApi.AssetsAssetsList(*j.ctx, &jms.AssetsAssetsListOpts{
		Offset: optional.NewInt32(offset),
		Limit:  optional.NewInt32(limit),
	})
	if err != nil {
		return nil, err
	}
	return res.Results, nil
}

//GetAdminUserID from jumpserver
func (j *JS) GetAdminUserID(adminUserName string) (string, error) {
	defer j.refreshAccessToken()
	res, _, err := j.client.AssetsAdminUsersApi.AssetsAdminUsersList(*j.ctx, &jms.AssetsAdminUsersListOpts{
		Name:   optional.NewString(adminUserName),
		Limit:  optional.NewInt32(65535),
		Offset: optional.NewInt32(0),
	})
	if err != nil {
		return "", err
	}
	id := ""
	for _, user := range res.Results {
		if user.Name == adminUserName {
			id = user.Id
		}
	}
	if id == "" {
		return id, fmt.Errorf("can not found admin-user which name is %s", adminUserName)
	}
	return id, nil
}

//AddAsset to jumpserver
func (j *JS) AddAsset(asset jms.Asset) (string, error) {
	defer j.refreshAccessToken()
	asset, _, err := j.client.AssetsAssetsApi.AssetsAssetsCreate(*j.ctx, asset)
	if err != nil {
		return "", err
	}
	return asset.Id, nil
}

//HasAsset return if asset already exists
func (j *JS) HasAsset(hostname string) (bool, error) {
	defer j.refreshAccessToken()
	_, err := j.GetAssetByHostname(hostname)
	if _, ok := err.(*AssetNotFoundError); ok {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

//AddAssetToUser to jumpserver
func (j *JS) AddAssetToUser(username string, assetID string) error {
	defer j.refreshAccessToken()
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
	defer j.refreshAccessToken()
	asset, _, err := j.client.AssetsAssetsApi.AssetsAssetsList(*j.ctx, &jms.AssetsAssetsListOpts{
		Hostname: optional.NewString(hostname),
		Limit:    optional.NewInt32(65535),
		Offset:   optional.NewInt32(0),
	})
	if err != nil {
		return jms.Asset{}, err
	}
	res := asset.Results
	if len(res) > 0 {
		return res[0], nil
	}
	return jms.Asset{}, &AssetNotFoundError{hostname}
}

//AssetNotFoundError is error for asset not found in jumpserver
type AssetNotFoundError struct {
	hostname string
}

func (e *AssetNotFoundError) Error() string {
	return fmt.Sprintf("can not found asset which hostnamem is %s", e.hostname)
}

//DelAsset from jumpserver
func (j *JS) DelAsset(asset jms.Asset) error {
	defer j.refreshAccessToken()
	asset, err := j.GetAssetByHostname(asset.Hostname)
	if _, ok := err.(*AssetNotFoundError); ok {
		return nil
	}
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
