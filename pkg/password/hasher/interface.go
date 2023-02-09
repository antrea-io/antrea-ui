package hasher

//go:generate mockgen -source=interface.go -package=testing -destination=testing/mock_interface.go
type Interface interface {
	Hash(password []byte, salt []byte) ([]byte, error)
}
