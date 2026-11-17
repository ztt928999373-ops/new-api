// Copyright (c) QuantumNous and contributors.
package totpops

import (
	"flag"
	"io"
	"path/filepath"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// Dispatch accepts local SQLite paths only. Secrets/DSNs never enter arguments.
func Dispatch(args []string, out io.Writer) error {
	if len(args) < 1 {
		return model.ErrTOTPStorage
	}
	flags := flag.NewFlagSet("totp", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	database := flags.String("sqlite", "", "")
	keys := flags.String("keys", "", "")
	destination := flags.String("destination", "", "")
	dbBackup := flags.String("db-backup", "", "")
	keyBackup := flags.String("key-backup", "", "")
	environment := flags.String("environment", "", "")
	offline := flags.Bool("offline", false, "")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || !filepath.IsAbs(*keys) {
		return model.ErrTOTPStorage
	}
	switch args[0] {
	case "init-keys":
		return InitializeKeys(*keys, *environment)
	case "inventory":
		s, _, err := OpenKeys(*keys)
		if err != nil {
			return err
		}
		db, closeDB, err := openDatabase(*database)
		if err != nil {
			return err
		}
		defer closeDB()
		result, err := model.InventoryTOTP(db, s)
		if err != nil {
			return err
		}
		b, err := common.Marshal(result)
		if err != nil {
			return model.ErrTOTPStorage
		}
		_, err = out.Write(append(b, '\n'))
		return err
	case "initialize":
		if !*offline {
			return model.ErrTOTPStorage
		}
		release, err := Acquire(*keys)
		if err != nil {
			return err
		}
		defer release()
		return InitializeDatabase(*database, *keys)
	case "migrate":
		if !*offline {
			return model.ErrTOTPStorage
		}
		return Migrate(*database, *destination, *keys, *dbBackup, *keyBackup)
	case "resume":
		if !*offline {
			return model.ErrTOTPStorage
		}
		return Resume(*database, *keys, *dbBackup, *keyBackup)
	case "backup":
		if !*offline {
			return model.ErrTOTPStorage
		}
		return Backup(*database, *keys, *dbBackup, *keyBackup)
	case "restore":
		if !*offline {
			return model.ErrTOTPStorage
		}
		return Restore(*dbBackup, *keyBackup, *destination, *keys)
	case "rotate":
		if !*offline {
			return model.ErrTOTPStorage
		}
		return Rotate(*database, *keys)
	default:
		return model.ErrTOTPStorage
	}
}
