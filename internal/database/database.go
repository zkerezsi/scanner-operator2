package database

import (
	"fmt"

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type DatabaseType string

const (
	PostgreSQL DatabaseType = "postgres"
	MySQL      DatabaseType = "mysql"
	SQLite     DatabaseType = "sqlite"
)

func GetDialector(databaseType string, dsn string) (gorm.Dialector, error) {
	switch databaseType {
	case string(PostgreSQL):
		return postgres.Open(dsn), nil
	case string(MySQL):
		return mysql.Open(dsn), nil
	case string(SQLite):
		return sqlite.Open(dsn), nil
	default:
		return nil, fmt.Errorf("unsupported database type: %s", databaseType)
	}
}

func GetDatabase(databaseType string, dsn string) (*gorm.DB, error) {
	dialector, err := GetDialector(databaseType, dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to get dialector: %w", err)
	}

	db, err := gorm.Open(dialector, &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	if err := db.AutoMigrate(&Hello{}); err != nil {
		return nil, fmt.Errorf("failed to automigrate database: %w", err)
	}

	return db, nil
}
