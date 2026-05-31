package auth_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"portfolio-tracker/internal/auth"
	"portfolio-tracker/internal/database"
	"portfolio-tracker/internal/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"gorm.io/gorm"
)

var testDB *gorm.DB

func TestMain(m *testing.M) {
	ctx := context.Background()

	container, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("testdb"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.WithWaitStrategy(wait.ForListeningPort("5432/tcp")),
	)
	if err != nil {
		panic(fmt.Sprintf("failed to start postgres container: %v", err))
	}
	defer container.Terminate(ctx)

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		panic(err)
	}

	testDB, err = database.NewPostgresFromDSN(connStr)
	if err != nil {
		panic(err)
	}

	if err := database.Migrate(testDB); err != nil {
		panic(err)
	}

	m.Run()
}

func newSvc() *auth.Service {
	return auth.NewService(testDB, "test-secret-32-chars-long-enough!!", 15*time.Minute, 7*24*time.Hour)
}

func TestRegister_Success(t *testing.T) {
	svc := newSvc()
	t.Cleanup(func() { testDB.Exec("DELETE FROM users WHERE email = 'alice@example.com'") })

	user, err := svc.Register("alice@example.com", "password123")
	require.NoError(t, err)
	assert.NotEmpty(t, user.ID)
	assert.Equal(t, "alice@example.com", user.Email)

	var portfolios []model.Portfolio
	testDB.Where("user_id = ?", user.ID).Find(&portfolios)
	assert.Len(t, portfolios, 1)
}

func TestRegister_DuplicateEmail(t *testing.T) {
	svc := newSvc()
	t.Cleanup(func() { testDB.Exec("DELETE FROM users WHERE email = 'dup@example.com'") })

	_, err := svc.Register("dup@example.com", "pass")
	require.NoError(t, err)

	_, err = svc.Register("dup@example.com", "other")
	assert.ErrorIs(t, err, auth.ErrEmailTaken)
}

func TestLogin_Success(t *testing.T) {
	svc := newSvc()
	t.Cleanup(func() { testDB.Exec("DELETE FROM users WHERE email = 'bob@example.com'") })

	_, err := svc.Register("bob@example.com", "secret99")
	require.NoError(t, err)

	pair, err := svc.Login("bob@example.com", "secret99")
	require.NoError(t, err)
	assert.NotEmpty(t, pair.AccessToken)
	assert.NotEmpty(t, pair.RefreshToken)
}

func TestLogin_WrongPassword(t *testing.T) {
	svc := newSvc()
	t.Cleanup(func() { testDB.Exec("DELETE FROM users WHERE email = 'carol@example.com'") })

	_, err := svc.Register("carol@example.com", "correct")
	require.NoError(t, err)

	_, err = svc.Login("carol@example.com", "wrong")
	assert.ErrorIs(t, err, auth.ErrInvalidCredentials)
}

func TestLogin_UnknownEmail(t *testing.T) {
	svc := newSvc()
	_, err := svc.Login("ghost@example.com", "pass")
	assert.ErrorIs(t, err, auth.ErrInvalidCredentials)
}

func TestRefreshToken_Success(t *testing.T) {
	svc := newSvc()
	t.Cleanup(func() { testDB.Exec("DELETE FROM users WHERE email = 'dave@example.com'") })

	_, err := svc.Register("dave@example.com", "pass123")
	require.NoError(t, err)

	pair, err := svc.Login("dave@example.com", "pass123")
	require.NoError(t, err)

	newPair, err := svc.RefreshToken(pair.RefreshToken)
	require.NoError(t, err)
	assert.NotEmpty(t, newPair.AccessToken)
}

func TestValidateAccessToken(t *testing.T) {
	svc := newSvc()
	t.Cleanup(func() { testDB.Exec("DELETE FROM users WHERE email = 'eve@example.com'") })

	_, err := svc.Register("eve@example.com", "pass123")
	require.NoError(t, err)

	pair, err := svc.Login("eve@example.com", "pass123")
	require.NoError(t, err)

	claims, err := svc.ValidateAccessToken(pair.AccessToken)
	require.NoError(t, err)
	assert.NotEmpty(t, claims.UserID)
}
