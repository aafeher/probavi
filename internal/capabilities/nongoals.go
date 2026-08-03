package capabilities

// nongoals.go is the one part of the document that cannot be derived from
// code, in principle rather than for want of a registry: a non-goal is
// precisely something no code implements. The statements restate the
// binding non-goals of AGENTS.md §2.4 and the no-telemetry rule of §3.3,
// and docs/capabilities.md repeats them for consumers — a test pins every
// id below to that document, so the two cannot drift apart.
//
// They exist because the downstream rule is "claim nothing that is not in
// this file": without them a consumer knows what Probavi does, but not
// what it must never be said to do.

// Non-goal identifiers.
const (
	NonGoalBackupEngine       = "backup_engine"
	NonGoalScheduler          = "scheduler"
	NonGoalDatabaseHostDaemon = "database_host_daemon"
	NonGoalSecretsManagement  = "secrets_management"
	NonGoalTelemetry          = "telemetry"
	NonGoalWebUI              = "web_ui"
)

// NonGoals returns the declared non-goals in documentation order.
func NonGoals() []NonGoal {
	return []NonGoal{
		{
			ID:        NonGoalBackupEngine,
			Statement: "Probavi takes no backups. It verifies backups produced by other tools — pgBackRest, wal-g, Barman, mysqldump and their equivalents — which are its foundation, never its competitors.",
		},
		{
			ID:        NonGoalScheduler,
			Statement: "Probavi ships no scheduler and no daemon of its own. Drills are started by cron or a systemd timer, with a lock file and a timeout.",
		},
		{
			ID:        NonGoalDatabaseHostDaemon,
			Statement: "Probavi runs no agent or daemon on database hosts.",
		},
		{
			ID:        NonGoalSecretsManagement,
			Statement: "Probavi manages no secrets. It reads credentials from environment variables or files for the duration of one drill, and redacts them from logs and evidence.",
		},
		{
			ID:        NonGoalTelemetry,
			Statement: "Probavi has no telemetry and never phones home. It is a trust product; that is not negotiable.",
		},
		{
			ID:        NonGoalWebUI,
			Statement: "Probavi ships no web interface. The shipped surfaces are the command line, a Prometheus textfile, and webhooks.",
		},
	}
}
