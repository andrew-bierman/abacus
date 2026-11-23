//go:build !unix

package bd

const openNoBlockNoFollow = 0

func isSymlinkRefusal(error) bool { return false }
