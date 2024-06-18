// xattr.go - extended attribute support for go-walk
//
// (c) 2023- Sudhi Herle <sudhi@herle.net>
//
// Licensing Terms: GPLv2
//
// If you need a commercial license for this work, please contact
// the author.
//
// This software does not come with any express or implied
// warranty; it is provided "as is". No claim  is made to its
// suitability for any purpose.

package walk

import (
	"fmt"
	"strings"
)

type Xattr map[string]string

func (x Xattr) String() string {
	var s strings.Builder
	for k, v := range x {
		s.WriteString(fmt.Sprintf("%s=%s\n", k, v))
	}
	return s.String()
}

// NewXattr returns the extended attributes of file 'fn'
func GetXattr(fn string) (Xattr, error) {
	return getxattr(fn)
}

// SetXattr sets the extended attributes of file 'fn' with
// the data in 'x'
func SetXattr(fn string, x Xattr) error {
	return setxattr(fn, x)
}

// DelXattr deletes the extended attributes in 'x' from file 'fn'
func DelXattr(fn string, x Xattr) error {
	return delxattr(fn, x)
}
