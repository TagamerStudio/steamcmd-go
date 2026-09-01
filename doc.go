// Package steamcmd provides a small, safe Go client for SteamCMD.
//
// A Client is intended to be the sole coordinator for one SteamCMD directory.
// The package serializes operations within a Client, but callers must also
// coordinate separate processes that use the same directories.
package steamcmd
