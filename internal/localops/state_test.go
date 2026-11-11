// Copyright (c) QuantumNous and contributors.
package localops

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/credentialcrypto"
	"github.com/QuantumNous/new-api/service/credentials"
	"github.com/stretchr/testify/require"
)

func TestLocalInstallBackupRestore(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	require.NoError(t, Initialize(source, "test"))
	require.NoError(t, Check(source))
	before, err := fileHash(filepath.Join(source, ringName))
	require.NoError(t, err)
	require.Error(t, Initialize(source, "test"))
	after, err := fileHash(filepath.Join(source, ringName))
	require.NoError(t, err)
	require.Equal(t, before, after, "never replace an existing ring")
	p, err := credentialcrypto.NewLocalFileKeyProvider(filepath.Join(source, ringName))
	require.NoError(t, err)
	oldVersion, err := p.GetKeyVersion(context.Background())
	require.NoError(t, err)
	db, closeDB, err := openDatabase(filepath.Join(source, databaseName))
	require.NoError(t, err)
	store := &credentials.Store{DB: db, Environment: "test", ProviderID: "local-test", Provider: p}
	actor := credentials.Principal{TenantID: "default", UserID: "synthetic-operator", PlatformOperator: true}
	active, err := store.Create(context.Background(), actor, true, "openai", "api_key", "relay", []byte("synthetic-recovery-active"))
	require.NoError(t, err)
	revoked, err := store.Create(context.Background(), actor, true, "openai", "api_key", "relay", []byte("synthetic-recovery-revoked"))
	require.NoError(t, err)
	require.NoError(t, store.SetStatus(context.Background(), actor, revoked.ID, 1, "revoked"))
	ch := model.Channel{Name: "synthetic-recovery-channel", Type: constant.ChannelTypeOpenAI, CredentialRef: active.ID, Status: common.ChannelStatusManuallyDisabled, Group: "openai-pilot", Models: "synthetic-model"}
	require.NoError(t, db.Create(&ch).Error)
	require.NoError(t, db.Create(&model.Ability{ChannelId: ch.Id, Group: ch.Group, Model: ch.Models, Enabled: false}).Error)
	policy := model.UpstreamBudget{ChannelID: ch.Id, Enabled: true, LimitMicros: 100, AttemptCeilingMicros: 10, Model: "synthetic-model", MaxRequestBytes: 4096, MaxOutputTokens: 32, ValidUntil: time.Now().Unix() + 3600, CeilingConfirmed: true, CostBasisHash: strings.Repeat("a", 64)}
	require.NoError(t, model.ConfigureUpstreamBudget(context.Background(), db, policy, 0))
	policy.Revision = 1
	_, err = model.ReserveUpstreamCost(context.Background(), db, policy)
	require.NoError(t, err)
	// Model an earlier supported snapshot missing an additive business table.
	require.NoError(t, db.Migrator().DropTable(&model.LoginEncryptionKey{}))
	closeDB()
	oldManifest, err := readManifest(filepath.Join(source, readyName))
	require.NoError(t, err)
	oldManifest.Build = "synthetic-previous-build"
	require.NoError(t, os.Remove(filepath.Join(source, readyName)))
	require.NoError(t, saveManifest(filepath.Join(source, readyName), oldManifest))
	handled, release, err := Dispatch([]string{"new-api", "serve", "--state", source})
	require.True(t, handled)
	require.Nil(t, release)
	require.Error(t, err, "a different binary must use staged restore instead of in-place migration")
	databaseDir, keyDir := filepath.Join(root, "database-backups"), filepath.Join(root, "key-backups")
	require.NoError(t, credentialcrypto.InitializeLocalDirectory(databaseDir))
	require.NoError(t, credentialcrypto.InitializeLocalDirectory(keyDir))
	databaseBackup, keyBackup := filepath.Join(databaseDir, "snapshot.sqlite"), filepath.Join(keyDir, "snapshot.ring")
	require.NoError(t, Backup(source, databaseBackup, keyBackup))
	require.Error(t, Backup(source, databaseBackup, keyBackup))
	target := filepath.Join(root, "restored")
	require.NoError(t, Restore(target, databaseBackup, keyBackup))
	require.NoError(t, Check(target))
	newManifest, err := readManifest(filepath.Join(target, readyName))
	require.NoError(t, err)
	require.Equal(t, common.Version, newManifest.Build)
	recovered, err := credentialcrypto.NewLocalFileKeyProvider(filepath.Join(target, ringName))
	require.NoError(t, err)
	newVersion, err := recovered.GetKeyVersion(context.Background())
	require.NoError(t, err)
	require.NotEqual(t, oldVersion, newVersion, "restores must rotate the active KEK before writes")
	restoredDB, closeRestored, err := openDatabase(filepath.Join(target, databaseName))
	require.NoError(t, err)
	defer closeRestored()
	require.True(t, restoredDB.Migrator().HasTable(&model.LoginEncryptionKey{}), "staged upgrade recreates additive table")
	restoredStore := &credentials.Store{DB: restoredDB, Environment: "test", ProviderID: "local-test", Provider: recovered}
	resolver := &credentials.Resolver{Environment: "test", Repository: restoredStore, Providers: map[string]credentialcrypto.KeyProvider{"local-test": recovered}, Audit: restoredStore}
	require.NoError(t, resolver.WithCredential(context.Background(), actor, active.ID, 1, "relay", func(secret []byte) error { require.Equal(t, "synthetic-recovery-active", string(secret)); return nil }))
	used := false
	require.Error(t, resolver.WithCredential(context.Background(), actor, revoked.ID, 1, "relay", func([]byte) error { used = true; return nil }))
	require.False(t, used)
	var saved model.Channel
	require.NoError(t, restoredDB.First(&saved, ch.Id).Error)
	require.Empty(t, saved.Key)
	require.Equal(t, active.ID, saved.CredentialRef)
	require.Equal(t, common.ChannelStatusManuallyDisabled, saved.Status)
	require.True(t, saved.UpstreamBudgetEnabled)
	var restoredBudget model.UpstreamBudget
	require.NoError(t, restoredDB.First(&restoredBudget, "channel_id = ?", ch.Id).Error)
	require.False(t, restoredBudget.Enabled, "restoring an old snapshot must not reopen a payable budget")
	require.EqualValues(t, 10, restoredBudget.HeldMicros, "restore never releases in-flight liability")
	require.Error(t, Restore(target, databaseBackup, keyBackup))
	// A fresh post-restore write and second backup are safe with the new KEK.
	_, err = restoredStore.Create(context.Background(), actor, true, "openai", "api_key", "relay", []byte("synthetic-post-restore"))
	require.NoError(t, err)
}

func TestRecoveryFaultsNeverPublishOrOverwrite(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "source")
	require.NoError(t, Initialize(state, "test"))
	dbDir, keyDir := filepath.Join(root, "db"), filepath.Join(root, "keys")
	require.NoError(t, credentialcrypto.InitializeLocalDirectory(dbDir))
	require.NoError(t, credentialcrypto.InitializeLocalDirectory(keyDir))
	dbBackup, ringBackup := filepath.Join(dbDir, "backup.sqlite"), filepath.Join(keyDir, "backup.ring")
	require.NoError(t, Backup(state, dbBackup, ringBackup))
	release, err := acquire(state)
	require.NoError(t, err)
	require.Error(t, Check(state))
	require.Error(t, Backup(state, filepath.Join(dbDir, "busy.sqlite"), filepath.Join(keyDir, "busy.ring")))
	release()
	require.NoError(t, Check(state))
	interrupted := filepath.Join(root, "interrupted")
	require.NoError(t, createState(interrupted))
	require.NoError(t, copyNew(dbBackup, filepath.Join(interrupted, databaseName)))
	require.Error(t, Check(interrupted))
	handled, unlock, err := Dispatch([]string{"new-api", "serve", "--state", interrupted})
	require.True(t, handled)
	require.Nil(t, unlock)
	require.Error(t, err)
	require.Error(t, Restore(interrupted, dbBackup, ringBackup))
	// Wrong but structurally valid KEK: bypass only the unkeyed checksum in the
	// TEST manifest, proving the authenticated probe rejects the mismatched ring.
	wrong := filepath.Join(keyDir, "wrong.ring")
	require.NoError(t, credentialcrypto.InitializeLocalFile(wrong))
	manifest, err := readManifest(dbBackup + ".manifest.json")
	require.NoError(t, err)
	wrongHash, err := fileHash(wrong)
	require.NoError(t, err)
	manifest.KeySHA256 = wrongHash
	require.NoError(t, os.Remove(dbBackup+".manifest.json"))
	require.NoError(t, saveManifest(dbBackup+".manifest.json", manifest))
	wrongTarget := filepath.Join(root, "wrong-key")
	require.Error(t, Restore(wrongTarget, dbBackup, wrong))
	_, err = os.Stat(filepath.Join(wrongTarget, readyName))
	require.True(t, os.IsNotExist(err))
	require.NoError(t, Check(state))
	// Missing ring and corrupted database both fail closed.
	require.NoError(t, os.Rename(filepath.Join(state, ringName), filepath.Join(state, "missing.ring")))
	require.Error(t, Check(state))
	require.NoError(t, os.Rename(filepath.Join(state, "missing.ring"), filepath.Join(state, ringName)))
	require.NoError(t, os.WriteFile(dbBackup, []byte("interrupted database copy"), 0600))
	manifest.DatabaseSHA256, err = fileHash(dbBackup)
	require.NoError(t, err)
	manifest.KeySHA256, err = fileHash(ringBackup)
	require.NoError(t, err)
	require.NoError(t, os.Remove(dbBackup+".manifest.json"))
	require.NoError(t, saveManifest(dbBackup+".manifest.json", manifest))
	corruptTarget := filepath.Join(root, "corrupt")
	require.Error(t, Restore(corruptTarget, dbBackup, ringBackup))
	_, err = os.Stat(filepath.Join(corruptTarget, readyName))
	require.True(t, os.IsNotExist(err))
	require.NoError(t, Check(state))
}

func TestStateLockReleasedAfterProcessDeath(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	require.NoError(t, Initialize(state, "test"))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStateLockChild$")
	cmd.Env = append(os.Environ(), "SUANJIE_LOCK_TEST_CHILD="+state)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	defer func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	ready := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(stdout).ReadString('\n'); ready <- line }()
	select {
	case line := <-ready:
		require.Equal(t, "locked\n", line)
	case <-ctx.Done():
		t.Fatal("child lock barrier timed out")
	}
	_, err = acquire(state)
	require.Error(t, err)
	require.NoError(t, cmd.Process.Kill())
	require.Error(t, cmd.Wait())
	require.NoError(t, Check(state), "OS lock must release without deleting a stale sentinel")
}

func TestStateLockChild(t *testing.T) {
	state := os.Getenv("SUANJIE_LOCK_TEST_CHILD")
	if state == "" {
		t.Skip("subprocess helper")
	}
	release, err := acquire(state)
	require.NoError(t, err)
	defer release()
	fmt.Println("locked")
	select {}
}
