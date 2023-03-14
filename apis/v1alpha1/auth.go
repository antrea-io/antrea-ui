package v1alpha1

type Token struct {
	TokenType   string `json:"tokenType"`
	AccessToken string `json:"accessToken"`
	ExpiresIn   int64  `json:"expiresIn"`
}
