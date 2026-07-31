package main

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
)

// Physical restore (pgbackrest source kind): unlike pg_restore, a pgBackRest
// restore replaces the data directory, so the engine must NOT be running.
// The drill config must start the sandbox idle (docker provider:
// `command: sleep infinity`) with an image that contains postgres,
// pgbackrest, and gosu. The adapter then owns the whole lifecycle:
// transfer repo → write config → restore → open sandbox-local auth →
// start the server → wait for recovery to finish.
const pgdataDir = "/var/lib/postgresql/data"

var stanzaPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// provisionPhysical runs the pgbackrest provision flow and returns the
// §6.2 response payload.
func provisionPhysical(ctx context.Context, c *core, req *provisionRequest, src *resolvedSource, logger *slog.Logger) (any, *protoError) {
	stanza := req.Source.Params["stanza"]
	if !stanzaPattern.MatchString(stanza) {
		return nil, protoErr("invalid_request", false,
			"pgbackrest source requires source.params.stanza (letters, digits, - and _)")
	}
	scratch := req.Sandbox.ScratchDir
	if scratch == "" {
		scratch = "/tmp"
	}
	repoInSandbox := scratch + "/probavi-pgbackrest-repo"

	if perr := checkIdleSandbox(ctx, c); perr != nil {
		return nil, perr
	}

	put, perr := c.putFile(ctx, putFileArgs{SourcePath: src.path, DestPath: repoInSandbox, Mode: "0755"})
	if perr != nil {
		return nil, perr
	}

	if perr := prepareRestore(ctx, c, repoInSandbox, stanza); perr != nil {
		return nil, perr
	}

	restore, stderr, perr := execChecked(ctx, c,
		"gosu", "postgres", "pgbackrest", "--stanza="+stanza, "restore")
	if perr != nil {
		return nil, perr
	}
	if restore.ExitCode != 0 {
		return nil, protoErr("restore_failed", false, "pgbackrest restore failed: %s", firstLine(stderr))
	}
	logger.Info("pgbackrest restore complete", "seconds", restore.DurationSeconds)

	readySeconds, perr := startEngine(ctx, c)
	if perr != nil {
		return nil, perr
	}
	logger.Info("engine recovered and ready", "seconds", readySeconds)

	return map[string]any{
		"connection": map[string]any{
			"scheme": "postgresql", "host": "127.0.0.1", "port": defaultPort,
			"database": defaultDatabase, "user": defaultUser,
		},
		"source_identity": map[string]any{
			"checksum": src.checksum, "size_bytes": src.sizeBytes, "created_at": src.createdAt,
		},
		"timings": map[string]any{
			"engine_ready_seconds": readySeconds,
			"transfer_seconds":     put.DurationSeconds,
			"restore_seconds":      restore.DurationSeconds,
		},
		"state": map[string]any{
			"database": defaultDatabase, "user": defaultUser, "mode": "physical", "stanza": stanza,
		},
	}, nil
}

// checkIdleSandbox verifies the preconditions of a physical restore: no
// engine running, pgbackrest present.
func checkIdleSandbox(ctx context.Context, c *core) *protoError {
	ready, _, perr := execChecked(ctx, c, "pg_isready", "-h", "127.0.0.1", "-U", defaultUser, "-q")
	if perr != nil {
		return perr
	}
	if ready.ExitCode == 0 {
		return protoErr("invalid_request", false,
			"pgbackrest restore needs an idle sandbox: set sandbox params command to keep the engine stopped (docker: command: sleep infinity)")
	}
	version, stderr, perr := execChecked(ctx, c, "pgbackrest", "version")
	if perr != nil {
		return perr
	}
	if version.ExitCode != 0 {
		return protoErr("invalid_request", false,
			"sandbox image lacks pgbackrest (%s): use an image with postgres, pgbackrest, and gosu", firstLine(stderr))
	}
	return nil
}

// prepareRestore writes the pgbackrest config, empties the data directory,
// hands the repo to the postgres user, and opens sandbox-local trust auth
// for after the restore. All paths are adapter-controlled constants; the
// stanza is pattern-validated.
func prepareRestore(ctx context.Context, c *core, repo, stanza string) *protoError {
	script := fmt.Sprintf(
		`set -e
mkdir -p /etc/pgbackrest
printf '[global]\nrepo1-path=%s\n\n[%s]\npg1-path=%s\n' > /etc/pgbackrest/pgbackrest.conf
rm -rf %s/* %s/.[!.]* 2>/dev/null || true
chown -R postgres:postgres %s %s /etc/pgbackrest`,
		repo, stanza, pgdataDir, pgdataDir, pgdataDir, repo, pgdataDir)
	res, stderr, perr := execChecked(ctx, c, "sh", "-c", script)
	if perr != nil {
		return perr
	}
	if res.ExitCode != 0 {
		return protoErr("internal", false, "prepare restore environment: %s", firstLine(stderr))
	}
	return nil
}

// startEngine overwrites pg_hba with sandbox-local trust (the restored
// cluster's auth config expects credentials this drill does not have — the
// sandbox has no network exposure, so trust is confined to the container),
// starts the server, and waits until recovery finishes and queries are
// accepted. Returns the measured wait in seconds.
func startEngine(ctx context.Context, c *core) (float64, *protoError) {
	script := fmt.Sprintf(
		`set -e
printf 'local all all trust\nhost all all 127.0.0.1/32 trust\nhost all all ::1/128 trust\n' > %s/pg_hba.conf
chown postgres:postgres %s/pg_hba.conf`, pgdataDir, pgdataDir)
	if res, stderr, perr := execChecked(ctx, c, "sh", "-c", script); perr != nil {
		return 0, perr
	} else if res.ExitCode != 0 {
		return 0, protoErr("internal", false, "write sandbox auth config: %s", firstLine(stderr))
	}

	start, stderr, perr := execChecked(ctx, c,
		"gosu", "postgres", "pg_ctl", "-D", pgdataDir, "-w", "-t", "600", "-l", "/tmp/probavi-pg.log", "start")
	if perr != nil {
		return 0, perr
	}
	if start.ExitCode != 0 {
		return 0, protoErr("restore_failed", false, "restored cluster failed to start: %s", firstLine(stderr))
	}
	readySeconds, perr := awaitEngine(ctx, c, defaultUser)
	if perr != nil {
		return 0, perr
	}
	return start.DurationSeconds + readySeconds, nil
}

// execChecked wraps core.exec returning the value and raw stderr.
func execChecked(ctx context.Context, c *core, argv ...string) (*execValue, []byte, *protoError) {
	val, _, stderr, perr := c.exec(ctx, execArgs{Argv: argv})
	if perr != nil {
		return nil, nil, perr
	}
	return val, stderr, nil
}
