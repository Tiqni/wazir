package github

import "testing"

func TestSplitRepoRejectsExtraSlash(t *testing.T) {
	for _, in := range []string{"owner/repo/extra", "owner", "/repo", "owner/", ""} {
		if _, _, err := splitRepo(in); err == nil {
			t.Errorf("splitRepo(%q) = nil error, want error", in)
		}
	}
	o, n, err := splitRepo("octocat/hello")
	if err != nil || o != "octocat" || n != "hello" {
		t.Errorf("splitRepo(octocat/hello) = %q,%q,%v", o, n, err)
	}
}
