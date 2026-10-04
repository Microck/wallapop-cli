// Package bundle identifies the exact upstream engine used by both the SDK and daemon.
package bundle

import _ "embed"

//go:embed LICENSE
var License []byte

const Module = "github.com/Microck/galleton"
const Version = "v0.0.0-20261004115250-efe7b742536d"
const Sum = "h1:ZGHG3/JvJPX2N+usV0KrSYGsueorzoDaZL+fblpqU84="
const ModSum = "h1:hp07LS66wDIysy70Z7P0Q7WOZEFP63hYsVC7gx4B+yk="
