package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
)

// BootstrapDevelopment runs after schema migrations, only with development login
// enabled. Existing accounts, credentials and scope grants are never reset.
func (s *Service) BootstrapDevelopment(ctx context.Context, createFile func([]byte) (rollback func() error, err error)) error {
	var rollback func() error
	err := s.provisionDevelopment(ctx, []string{"dev-dorm-1"}, func(credentials []DevelopmentCredential) error {
		var body bytes.Buffer
		fmt.Fprintln(&body, "Тестовые учётные записи — только для development")
		fmt.Fprintln(&body, "Вход: /auth/login")
		fmt.Fprintln(&body, "Назначенное общежитие сотрудников: dev-dorm-1")
		for _, credential := range credentials {
			fmt.Fprintf(&body, "\nРоль: %s\nЛогин: %s\nПароль: %s\nID: %s\n", credential.Role, credential.Login, credential.Password, credential.UserID)
		}
		var err error
		rollback, err = createFile(body.Bytes())
		// The file adapter cleans up its own failed writes.
		if err != nil {
			rollback = nil
		}
		return err
	}, true)
	if err != nil {
		if rollback != nil {
			_ = rollback()
		}
		// SQL and filesystem errors may include sensitive values. Do not return
		// them to the startup logger.
		return errors.New("development user setup failed: require an empty user batch and a new writable DEV_USERS_FILE; preserve existing credentials")
	}
	return nil
}
