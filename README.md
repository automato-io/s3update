# s3update

__Enable your Golang applications to self update with S3. Requires Go 1.8+__

This package enables our internal tools to be updated when new commits to their master branch are pushed to Github.

Latest binaries are hosted on S3 under a specific bucket along its current version. When ran locally, the binary will
fetch the version and if its local version is older than the remote one, the new binary will get fetched and will exit,
stating to the user that it got updated and need to be ran again.

In our case, we're only shipping Linux and Darwin, targeting amd64 platform.

Bucket will have the following structure:

```
mybucket/
  mytool/
	VERSION
	mytool-linux-amd64
	mytool-darwin-amd64
```

## Usage

### Example

```go
package main

import (
	"github.com/automato-io/s3update"
)

var (
	// This gets set during the compilation. See below.
	Version = ""
)

func main() {
	err := s3update.AutoUpdate(s3update.Updater{
		CurrentVersion: Version,
		S3Bucket:       "mybucket",
		S3Region:       "eu-west-1",
		S3ReleaseKey:   "mytool/mytool-{{OS}}-{{ARCH}}",
		S3VersionKey:   "mytool/VERSION",
	})

  if err != nil {
    // ...
  }

  ...
}
```

## Copyright

Copyright © 2016 Heetch

See the [LICENSE](https://github.com/heetch/s3update/blob/master/LICENSE) (MIT) file for more details.



