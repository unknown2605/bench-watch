package transcripts

// Code is the scanner's error code for any error, for callers outside the package.
func Code(err error) string { return codeOf(err) }
