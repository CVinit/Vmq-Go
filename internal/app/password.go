package app

import "golang.org/x/crypto/bcrypt"

func hashAdminPassword(raw string) (string, error) {
	hashed, err := bcrypt.GenerateFromPassword([]byte(raw), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hashed), nil
}

func isPasswordHash(stored string) bool {
	_, err := bcrypt.Cost([]byte(stored))
	return err == nil
}

func verifyAdminPassword(stored, candidate string) bool {
	if isPasswordHash(stored) {
		return bcrypt.CompareHashAndPassword([]byte(stored), []byte(candidate)) == nil
	}
	return secureEqual(stored, candidate)
}
