// Copyright (c) QuantumNous and contributors.
package localops

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/credentialcrypto"
)

// Dispatch returns handled=false with a release function only for ops serve,
// which proceeds through the existing main application and its existing routes.
func Dispatch(args []string) (handled bool, release func(), err error) {
	if len(args) < 2 {
		return true, nil, ErrOperation
	}
	flags := flag.NewFlagSet("ops", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	state := flags.String("state", "", "")
	environment := flags.String("environment", "", "")
	databaseBackup := flags.String("database-backup", "", "")
	keyBackup := flags.String("key-backup", "", "")
	port := flags.Int("port", 3000, "")
	channelID := flags.Int("channel-id", 0, "")
	restoreCheck := flags.String("restore-check", "", "")
	if flags.Parse(args[2:]) != nil || flags.NArg() != 0 {
		return true, nil, ErrOperation
	}
	if *state == "" {
		return true, nil, ErrOperation
	}
	absolute, err := filepath.Abs(*state)
	if err != nil {
		return true, nil, ErrOperation
	}
	switch args[1] {
	case "private-dir":
		return true, nil, credentialcrypto.InitializeLocalDirectory(absolute)
	case "init":
		return true, nil, Initialize(absolute, *environment)
	case "check":
		return true, nil, Check(absolute)
	case "backup":
		return true, nil, Backup(absolute, *databaseBackup, *keyBackup)
	case "restore":
		return true, nil, Restore(absolute, *databaseBackup, *keyBackup)
	case "migrate-channel":
		return true, nil, MigrateChannel(absolute, *channelID, *databaseBackup, *keyBackup, *restoreCheck)
	case "serve":
		if *port < 1 || *port > 65535 {
			return true, nil, ErrOperation
		}
		release, err = acquire(absolute)
		if err != nil {
			return true, nil, err
		}
		fail := func() (bool, func(), error) { release(); return true, nil, ErrOperation }
		m, e := readManifest(filepath.Join(absolute, readyName))
		if e != nil || m.Build != common.Version || verify(absolute, m) != nil {
			return fail()
		}
		// This explicit profile selects SQLite, one local instance and no dotenv.
		// The default upstream startup path is unchanged for other deployments.
		variables := map[string]string{
			"SUANJIE_STATE_DIR": absolute, "SQL_DSN": "local", "LOG_SQL_DSN": "", "SQLITE_PATH": filepath.Join(absolute, databaseName),
			"SUPPLIER_CREDENTIAL_KEYRING_PATH": filepath.Join(absolute, ringName), "SUPPLIER_CREDENTIAL_ENVIRONMENT": m.Environment,
			"REDIS_CONN_STRING": "", "NODE_TYPE": "master", "MEMORY_CACHE_ENABLED": "true", "BATCH_UPDATE_ENABLED": "false",
			"BIND_ADDRESS": "127.0.0.1", "PORT": strconv.Itoa(*port), "ENABLE_PPROF": "false", "DEBUG": "false",
		}
		for key, value := range variables {
			if os.Setenv(key, value) != nil {
				return fail()
			}
		}
		if os.Chdir(absolute) != nil {
			return fail()
		}
		os.Args = []string{args[0], "--port", strconv.Itoa(*port), "--log-dir", filepath.Join(absolute, "logs")}
		return false, release, nil
	default:
		return true, nil, ErrOperation
	}
}
