// Copyright (c) QuantumNous and contributors.
package service

import (
	"context"
	"os"
	"strconv"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/credentialcrypto"
	"github.com/QuantumNous/new-api/service/credentials"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Credential wiring is explicit; startup does not migrate data or create KEKs.
var channelCredentialResolver atomic.Pointer[credentials.Resolver]

// Configuration is opt-in; this never creates KEKs or migrates legacy secrets.
func InitSupplierCredentialStorage(db *gorm.DB) error {
	path := os.Getenv("SUPPLIER_CREDENTIAL_KEYRING_PATH")
	if path == "" {
		return nil
	}
	environment := os.Getenv("SUPPLIER_CREDENTIAL_ENVIRONMENT")
	if environment != "development" && environment != "test" && environment != "production" {
		return credentialcrypto.ErrUnavailable
	}
	p, err := credentialcrypto.NewLocalFileKeyProvider(path)
	if err != nil {
		return credentialcrypto.ErrUnavailable
	}
	id := "local-" + environment
	s := &credentials.Store{DB: db, Environment: environment, ProviderID: id, Provider: p}
	if !db.Migrator().HasTable(&credentials.Head{}) || !db.Migrator().HasTable(&credentials.StoredVersion{}) || !db.Migrator().HasTable(&credentials.AccessEvent{}) {
		return credentialcrypto.ErrUnavailable
	}
	SetChannelCredentialResolver(&credentials.Resolver{Environment: environment, Repository: s, Providers: map[string]credentialcrypto.KeyProvider{id: p}, Audit: s})
	return nil
}

func SetChannelCredentialResolver(r *credentials.Resolver) *credentials.Resolver {
	return channelCredentialResolver.Swap(r)
}

func ConfiguredCredentialStore() *credentials.Store {
	r := channelCredentialResolver.Load()
	if r == nil {
		return nil
	}
	s, _ := r.Repository.(*credentials.Store)
	return s
}

// acquireChannelForRequest is private: plaintext is only exposed together with
// a request-lifetime lease through BindChannelCredentialRequest.
func acquireChannelForRequest(ctx context.Context, ch *model.Channel, userID int) (*model.Channel, *credentials.Lease, error) {
	if ch == nil {
		return nil, nil, credentialcrypto.ErrUnavailable
	}
	if ch.CredentialRef == "" {
		return ch, nil, nil
	}
	if (ch.Type != constant.ChannelTypeOpenAI && ch.Type != constant.ChannelTypeAli) || ch.Other != "" || ch.OtherSettings != "" || (ch.Setting != nil && *ch.Setting != "") || (ch.HeaderOverride != nil && *ch.HeaderOverride != "") || (ch.ParamOverride != nil && *ch.ParamOverride != "") {
		return nil, nil, credentialcrypto.ErrUnavailable
	}
	r := channelCredentialResolver.Load()
	if r == nil || userID <= 0 {
		return nil, nil, credentialcrypto.ErrUnavailable
	}
	if ch.Type == constant.ChannelTypeOpenAI {
		if store := ConfiguredCredentialStore(); store != nil {
			head, err := store.Metadata(ctx, credentials.Principal{TenantID: "default", UserID: strconv.Itoa(userID), PlatformOperator: true}, ch.CredentialRef)
			if err != nil || head.Provider == "bailian" {
				return nil, nil, credentialcrypto.ErrUnavailable
			}
		}
	}
	// Bailian is a fixed Beijing pilot, never a configurable OpenAI redirect.
	if ch.Type == constant.ChannelTypeAli {
		if ch.BaseURL == nil || *ch.BaseURL != "https://dashscope.aliyuncs.com" || ch.Models != "qwen3.7-flash-2026-07-15" || (ch.ModelMapping != nil && *ch.ModelMapping != "") || ch.ChannelInfo.IsMultiKey || common.TLSInsecureSkipVerify || common.DebugEnabled {
			return nil, nil, credentialcrypto.ErrUnavailable
		}
		store := ConfiguredCredentialStore()
		if store == nil {
			return nil, nil, credentialcrypto.ErrUnavailable
		}
		head, err := store.Metadata(ctx, credentials.Principal{TenantID: "default", UserID: strconv.Itoa(userID), PlatformOperator: true}, ch.CredentialRef)
		if err != nil || head.Provider != "bailian" || head.CredentialType != "api_key" {
			return nil, nil, credentialcrypto.ErrUnavailable
		}
	}
	copy := *ch
	copy.Key, copy.Keys = "", nil
	lease, err := r.AcquireCurrent(ctx, credentials.Principal{TenantID: "default", UserID: strconv.Itoa(userID), PlatformOperator: true}, ch.CredentialRef, "relay", func(secret []byte) error { copy.Key = string(secret); return nil })
	if err != nil {
		return nil, nil, credentialcrypto.ErrUnavailable
	}
	return &copy, lease, nil
}

type channelRequestLease struct {
	lease  *credentials.Lease
	parent context.Context
}

const channelRequestLeaseKey = "supplier_credential_request_lease"

// BindChannelCredentialRequest is the runtime entry point. Retries first release
// the previous lease and restore its parent context; they never reuse plaintext.
func BindChannelCredentialRequest(c *gin.Context, ch *model.Channel, userID int) (*model.Channel, error) {
	ReleaseChannelCredentialRequest(c)
	if c.Request == nil || c.Request.URL.Path != "/v1/chat/completions" {
		return nil, credentialcrypto.ErrUnavailable
	}
	parent := c.Request.Context()
	copy, lease, err := acquireChannelForRequest(parent, ch, userID)
	if err != nil {
		return nil, err
	}
	if lease != nil {
		if ch.Type == constant.ChannelTypeAli {
			c.Set("bailian_pilot_no_retry", true)
		}
		c.Set(channelRequestLeaseKey, channelRequestLease{lease: lease, parent: parent})
		c.Request = c.Request.WithContext(lease.Context())
	}
	return copy, nil
}

func ReleaseChannelCredentialRequest(c *gin.Context) {
	value, ok := c.Get(channelRequestLeaseKey)
	state, valid := value.(channelRequestLease)
	if !ok || !valid {
		return
	}
	state.lease.Release()
	if c.Request != nil {
		c.Request = c.Request.WithContext(state.parent)
	}
	c.Set(channelRequestLeaseKey, nil)
	c.Set(string(constant.ContextKeyChannelKey), "")
}

func WithConfiguredCredentialContext(ctx context.Context, actor credentials.Principal, id string, version uint64, use func(context.Context, []byte) error) error {
	r := channelCredentialResolver.Load()
	if r == nil {
		return credentialcrypto.ErrUnavailable
	}
	return r.WithCredentialContext(ctx, actor, id, version, "relay", use)
}
