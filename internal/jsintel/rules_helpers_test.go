package jsintel

import "testing"

func extractOne(t *testing.T, js string) Result {
	t.Helper()
	return Extract("app.example.com", "https://app.example.com/", "https://app.example.com/app.js", js)
}

func findIface(res Result, method, path string) *Interface {
	for i := range res.Interfaces {
		if res.Interfaces[i].Method == method && res.Interfaces[i].Path == path {
			return &res.Interfaces[i]
		}
	}
	return nil
}
