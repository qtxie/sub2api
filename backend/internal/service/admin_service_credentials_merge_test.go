//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type updateAccountCredsRepoStub struct {
	mockAccountRepoForGemini
	account     *Account
	updateCalls int
}

func (r *updateAccountCredsRepoStub) GetByID(ctx context.Context, id int64) (*Account, error) {
	return r.account, nil
}

func (r *updateAccountCredsRepoStub) Update(ctx context.Context, account *Account) error {
	r.updateCalls++
	r.account = account
	return nil
}

func TestUpdateAccount_PreservesSensitiveCredsWhenIncomingOmits(t *testing.T) {
	accountID := int64(202)
	repo := &updateAccountCredsRepoStub{
		account: &Account{
			ID:       accountID,
			Platform: PlatformAnthropic,
			Type:     AccountTypeOAuth,
			Status:   StatusActive,
			Credentials: map[string]any{
				"refresh_token": "rt-existing",
				"access_token":  "at-existing",
				"id_token":      "id-existing",
				"base_url":      "https://old.example.com",
			},
		},
	}
	svc := &adminServiceImpl{accountRepo: repo}

	// 模拟前端编辑：仅修改 base_url，没有传 token（脱敏后前端 spread 拿不到敏感键）
	updated, err := svc.UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Credentials: map[string]any{
			"base_url": "https://new.example.com",
		},
	})

	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, 1, repo.updateCalls)

	// 敏感键应保留
	require.Equal(t, "rt-existing", repo.account.Credentials["refresh_token"])
	require.Equal(t, "at-existing", repo.account.Credentials["access_token"])
	require.Equal(t, "id-existing", repo.account.Credentials["id_token"])
	// 非敏感键被替换
	require.Equal(t, "https://new.example.com", repo.account.Credentials["base_url"])
}

func TestUpdateAccount_ExplicitNewTokenOverwrites(t *testing.T) {
	accountID := int64(203)
	repo := &updateAccountCredsRepoStub{
		account: &Account{
			ID:       accountID,
			Platform: PlatformAnthropic,
			Type:     AccountTypeOAuth,
			Status:   StatusActive,
			Credentials: map[string]any{
				"refresh_token": "rt-old",
				"api_key":       "sk-old",
			},
		},
	}
	svc := &adminServiceImpl{accountRepo: repo}

	updated, err := svc.UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Credentials: map[string]any{
			"refresh_token": "rt-new",
			// api_key 没传 → 应保留旧值
		},
	})
	require.NoError(t, err)
	require.NotNil(t, updated)

	require.Equal(t, "rt-new", repo.account.Credentials["refresh_token"])
	require.Equal(t, "sk-old", repo.account.Credentials["api_key"])
}

func TestUpdateAccount_EmptyCredentialsSkipsUpdate(t *testing.T) {
	accountID := int64(204)
	repo := &updateAccountCredsRepoStub{
		account: &Account{
			ID:       accountID,
			Platform: PlatformAnthropic,
			Type:     AccountTypeOAuth,
			Status:   StatusActive,
			Credentials: map[string]any{
				"refresh_token": "rt-existing",
			},
		},
	}
	svc := &adminServiceImpl{accountRepo: repo}

	_, err := svc.UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Credentials: map[string]any{}, // len == 0 → 闸门跳过
		Name:        "renamed",
	})
	require.NoError(t, err)

	require.Equal(t, "rt-existing", repo.account.Credentials["refresh_token"], "空 credentials 不应触碰已有 token")
	require.Equal(t, "renamed", repo.account.Name)
}

func TestUpdateAccount_OpenAIAPIKeyListSaveSemantics(t *testing.T) {
	newRepo := func() *updateAccountCredsRepoStub {
		return &updateAccountCredsRepoStub{
			account: &Account{
				ID:       205,
				Platform: PlatformOpenAI,
				Type:     AccountTypeAPIKey,
				Status:   StatusActive,
				Credentials: map[string]any{
					"api_key":      "sk-primary",
					"api_key_list": []any{"sk-existing"},
					"base_url":     "https://api.openai.com",
				},
			},
		}
	}

	t.Run("omitted list is preserved", func(t *testing.T) {
		repo := newRepo()
		_, err := (&adminServiceImpl{accountRepo: repo}).UpdateAccount(context.Background(), repo.account.ID, &UpdateAccountInput{
			Credentials: map[string]any{"base_url": "https://upstream.example.com"},
		})
		require.NoError(t, err)
		require.Equal(t, []string{"sk-existing"}, repo.account.Credentials[OpenAIAPIKeyListCredentialKey])
	})

	t.Run("replacement is normalized", func(t *testing.T) {
		repo := newRepo()
		_, err := (&adminServiceImpl{accountRepo: repo}).UpdateAccount(context.Background(), repo.account.ID, &UpdateAccountInput{
			Credentials: map[string]any{
				OpenAIAPIKeyListCredentialKey: []any{" sk-next ", "sk-primary", "sk-next"},
			},
		})
		require.NoError(t, err)
		require.Equal(t, []string{"sk-next"}, repo.account.Credentials[OpenAIAPIKeyListCredentialKey])
	})

	t.Run("explicit empty list clears", func(t *testing.T) {
		repo := newRepo()
		_, err := (&adminServiceImpl{accountRepo: repo}).UpdateAccount(context.Background(), repo.account.ID, &UpdateAccountInput{
			Credentials: map[string]any{OpenAIAPIKeyListCredentialKey: []any{}},
		})
		require.NoError(t, err)
		require.Equal(t, []string{}, repo.account.Credentials[OpenAIAPIKeyListCredentialKey])
	})
}
