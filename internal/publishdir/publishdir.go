// Package publishdir moves an OwnGit-owned, freshly written directory to a new
// name. On Windows it never replaces an existing destination and briefly
// retries while another process, such as an antivirus scanner or search
// indexer, holds a file inside the directory. Elsewhere it is os.Rename.
package publishdir

import "time"

// RetryBound limits how long Rename keeps retrying a transient Windows denial.
const RetryBound = 2 * time.Second
