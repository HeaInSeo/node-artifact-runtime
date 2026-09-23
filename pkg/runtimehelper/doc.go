// Package runtimehelper implements the node-artifact-runtime shim that runs
// inside a DAG node container: it materializes inputs, supervises one user
// command, and emits the output manifest.
//
// # Process-wide child reaping
//
// While Run is supervising the user command, runtimehelper owns child reaping
// for the whole process. On Linux it marks the process as a child subreaper so
// that orphaned descendants of the command are reparented to it, and it
// collects every exited child with wait4(-1). wait4(-1) cannot tell the
// command's direct child and adopted descendants apart from unrelated children
// of the same process, so the status of any child that Run did not start is
// discarded.
//
// Callers must therefore not start or wait for any other child process (for
// example through os/exec) while Run is active. Such a concurrent waiter can
// lose its child's exit status or fail with ECHILD. The node-artifact-runtime
// binary meets this precondition: it calls Run once per process and starts no
// other child processes.
//
// # Bounded shutdown can leave an unreaped child
//
// Shutdown is bounded: after the grace period Run sends SIGKILL to the command's
// process group and waits only a fixed time for it to exit. A child that
// outlives that hard deadline (for example one blocked in uninterruptible I/O)
// is not waited for. Run stops reaping and returns, so when that child finally
// exits nobody collects its status and it stays a zombie of the calling
// process.
//
// After Run returns, the caller must therefore terminate the process (the
// node-artifact-runtime binary exits with Run's result). Do not reuse the same
// process to run Run again or to manage further child processes: an unreaped
// child from the previous Run may still be present, and its later exit is not
// handled. This is a library contract, not a guarantee that Run reaps every
// child it started.
package runtimehelper
