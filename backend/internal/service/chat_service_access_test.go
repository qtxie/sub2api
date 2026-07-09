package service

import (
	"context"
	"errors"
	"testing"
)

type chatAccessUserRepo struct {
	UserRepository
	user *User
	err  error
}

func (r *chatAccessUserRepo) GetByID(context.Context, int64) (*User, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.user, nil
}

func TestChatServiceEnsureUserCanUseChat(t *testing.T) {
	tests := []struct {
		name    string
		user    *User
		repoErr error
		wantErr error
	}{
		{name: "enabled", user: &User{ID: 1, ChatEnabled: true}},
		{name: "disabled", user: &User{ID: 1, ChatEnabled: false}, wantErr: ErrChatDisabled},
		{name: "repo error", repoErr: ErrUserNotFound, wantErr: ErrUserNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewChatService(nil, nil, &chatAccessUserRepo{user: tt.user, err: tt.repoErr})
			err := svc.EnsureUserCanUseChat(context.Background(), 1)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("EnsureUserCanUseChat() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
