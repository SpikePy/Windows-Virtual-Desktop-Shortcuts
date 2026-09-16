//go:build windows

package setup

import (
	"fmt"
	"io"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Downloads go through WinINet, Windows' own HTTP stack, rather than Go's
// net/http: that keeps several MB of TLS code out of the exe, and it uses
// the user's system proxy settings and Windows' certificate store.
var (
	modWininet = windows.NewLazySystemDLL("wininet.dll")

	procInternetOpenW       = modWininet.NewProc("InternetOpenW")
	procInternetSetOptionW  = modWininet.NewProc("InternetSetOptionW")
	procInternetOpenUrlW    = modWininet.NewProc("InternetOpenUrlW")
	procHttpQueryInfoW      = modWininet.NewProc("HttpQueryInfoW")
	procInternetReadFile    = modWininet.NewProc("InternetReadFile")
	procInternetCloseHandle = modWininet.NewProc("InternetCloseHandle")
)

const (
	internetOpenTypePreconfig = 0 // use the system's proxy configuration

	internetFlagReload       = 0x80000000 // always ask the server, never the cache
	internetFlagNoCacheWrite = 0x04000000
	internetFlagNoCookies    = 0x00080000
	internetFlagNoUI         = 0x00000200

	internetOptionConnectTimeout = 2
	internetOptionReceiveTimeout = 6

	httpQueryStatusCode = 19
	httpQueryFlagNumber = 0x20000000

	// httpTimeoutMs bounds connecting and each individual read, so a
	// stalled connection fails instead of hanging, while a slow but
	// progressing download is never cut off.
	httpTimeoutMs uint32 = 30_000
)

// httpGet fetches url with an HTTP GET, following redirects, and copies
// the response body to w. headers are extra request headers, each
// "Name: value". Any status other than 200 OK is an error, which includes
// the start of the response body.
func httpGet(url string, headers []string, w io.Writer) error {
	agent, err := windows.UTF16PtrFromString(userAgent)
	if err != nil {
		return err
	}
	inet, _, e := procInternetOpenW.Call(uintptr(unsafe.Pointer(agent)), internetOpenTypePreconfig, 0, 0, 0)
	if inet == 0 {
		return fmt.Errorf("InternetOpenW: %w", e)
	}
	defer procInternetCloseHandle.Call(inet)

	timeout := httpTimeoutMs
	for _, opt := range []uintptr{internetOptionConnectTimeout, internetOptionReceiveTimeout} {
		procInternetSetOptionW.Call(inet, opt, uintptr(unsafe.Pointer(&timeout)), unsafe.Sizeof(timeout))
	}

	urlPtr, err := windows.UTF16PtrFromString(url)
	if err != nil {
		return err
	}
	var headersPtr *uint16
	var headersLen uintptr
	if len(headers) > 0 {
		if headersPtr, err = windows.UTF16PtrFromString(strings.Join(headers, "\r\n")); err != nil {
			return err
		}
		headersLen = 0xFFFFFFFF // (DWORD)-1: the headers string is NUL-terminated
	}
	req, _, e := procInternetOpenUrlW.Call(inet,
		uintptr(unsafe.Pointer(urlPtr)), uintptr(unsafe.Pointer(headersPtr)), headersLen,
		internetFlagReload|internetFlagNoCacheWrite|internetFlagNoCookies|internetFlagNoUI, 0)
	if req == 0 {
		return fmt.Errorf("InternetOpenUrlW: %w", e)
	}
	defer procInternetCloseHandle.Call(req)

	var status uint32
	size := uint32(unsafe.Sizeof(status))
	if r, _, e := procHttpQueryInfoW.Call(req, httpQueryStatusCode|httpQueryFlagNumber,
		uintptr(unsafe.Pointer(&status)), uintptr(unsafe.Pointer(&size)), 0); r == 0 {
		return fmt.Errorf("HttpQueryInfoW: %w", e)
	}

	body := responseReader(req)
	if status != 200 {
		return fmt.Errorf("HTTP %d%s", status, errorSnippet(body))
	}
	_, err = io.Copy(w, body)
	return err
}

// errorSnippet returns the start of an error response's body as one short
// line for an error message ("" if the body is empty). GitHub's API sends
// a useful JSON message, but its 404 pages are whole HTML documents.
func errorSnippet(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 4096))
	s := strings.Join(strings.Fields(string(b)), " ")
	if s == "" {
		return ""
	}
	const limit = 200
	if runes := []rune(s); len(runes) > limit {
		s = string(runes[:limit]) + "..."
	}
	return ": " + s
}

// responseReader reads a WinINet request handle's response body.
type responseReader uintptr

func (h responseReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	var n uint32
	if r, _, e := procInternetReadFile.Call(uintptr(h),
		uintptr(unsafe.Pointer(&p[0])), uintptr(len(p)), uintptr(unsafe.Pointer(&n))); r == 0 {
		return 0, fmt.Errorf("InternetReadFile: %w", e)
	}
	if n == 0 {
		return 0, io.EOF
	}
	return int(n), nil
}
