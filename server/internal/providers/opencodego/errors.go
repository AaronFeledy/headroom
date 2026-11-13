package opencodego

import "errors"

var (
	errCredentialMissing    = errors.New("opencodego: credential missing")
	errCredentialExpired    = errors.New("opencodego: credential expired")
	errCredentialAmbiguous  = errors.New("opencodego: credential ambiguous")
	errCredentialInvalid    = errors.New("opencodego: credential invalid")
	errCredentialUnreadable = errors.New("opencodego: credential unreadable")
	errUsageResponse        = errors.New("opencodego: usage response changed")
)
