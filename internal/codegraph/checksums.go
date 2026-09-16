package codegraph

// Generated; do not edit manually. These are the SHA-256 checksums for the pinned
// codegraph release (see Version in install.go).

var releaseAssetSHA256 = map[string]string{
	"codegraph-darwin-arm64.tar.gz": "347e57598fc3e017f617005f3e09fa2304b353dd3b585d898ed3bd6fc256c245",
	"codegraph-darwin-x64.tar.gz":   "ab8737e788ad06c8d78506a308337b14195a2fb8cf6ee110cc7d84c63d21ba49",
	"codegraph-linux-arm64.tar.gz":  "bfa23de555ab67bb2a6380a089b9f38a07388598f15d610276190de56292211a",
	"codegraph-linux-x64.tar.gz":    "56cce751fe97d5464147e97f9679cc0d4de2ecccb696d579dd271cf6f551c976",
	"codegraph-win32-arm64.zip":     "c5d3890d461783f294a5e2bb5e56e857dd98df7788b1891c0519290132ccab30",
	// win32-x64 is REBUILT from the self-contained @colbymchenry/codegraph-win32-x64
	// npm bundle (node.exe + lib/ + bin/) and vendored as assets/codegraph_runtime.bin
	// for the portable `-tags codegraph_embed` build. Its SHA256 is recorded here so
	// the embed path passes the same integrity gate a download would — the shipped
	// exe installs the runtime with zero network. Regenerate with
	// .cache/build-codegraph-zip.py if the vendored bundle is refreshed.
	"codegraph-win32-x64.zip": "4f5e5bd4c4074ffe67f172e368515ec485271932a29666be62be17ce07f56c42",
}
