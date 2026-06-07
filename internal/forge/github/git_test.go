package github

import (
	"strings"
	"testing"
)

func TestAuthConfigEnvBuildsExtraHeader(t *testing.T) {
	env := authConfigEnv("ghp_secret")
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "GIT_CONFIG_KEY_0=http.extraHeader") {
		t.Errorf("missing extraHeader key:\n%s", joined)
	}
	// base64("x-access-token:ghp_secret")
	if !strings.Contains(joined, "GIT_CONFIG_VALUE_0=AUTHORIZATION: basic eC1hY2Nlc3MtdG9rZW46Z2hwX3NlY3JldA==") {
		t.Errorf("wrong/absent basic header:\n%s", joined)
	}
	if !strings.Contains(joined, "GIT_CONFIG_COUNT=1") {
		t.Errorf("missing GIT_CONFIG_COUNT:\n%s", joined)
	}
}

func TestAuthConfigEnvEmptyTokenIsNil(t *testing.T) {
	if authConfigEnv("") != nil {
		t.Error("empty token must yield no auth env")
	}
}
