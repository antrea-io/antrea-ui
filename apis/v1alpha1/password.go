package v1alpha1

type UpdatePassword struct {
	// base64-encoded passwords
	CurrentPassword []byte `json:"currentPassword"`
	NewPassword     []byte `json:"newPassword"`
}
