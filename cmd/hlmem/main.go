// Command hlmem searches and dumps another process's memory — the reverse-
// engineering tool for locating room-participant data (nicknames, battle codes,
// ping) inside StarCraft.
//
// Usage:
//   hlmem <pid> search <text>          find ASCII/UTF-8 and UTF-16LE occurrences
//   hlmem <pid> dump <hexaddr> <size>  hex+ascii dump at an address
//   hlmem <pid> regions                list committed readable regions
package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/hlauncher/hlauncher/internal/core/paths"
	"github.com/hlauncher/hlauncher/internal/identity"
	"github.com/hlauncher/hlauncher/internal/roster"
)

func identityPath() string {
	p, err := paths.New()
	if err != nil {
		return "identities.json"
	}
	_ = p.EnsureDirectoriesExist()
	return filepathJoin(p.DataDir, "identities.json")
}

func filepathJoin(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + string(os.PathSeparator) + name
}

var (
	modk32           = windows.NewLazySystemDLL("kernel32.dll")
	procVirtualQuery = modk32.NewProc("VirtualQueryEx")
)

type memBasicInfo struct {
	BaseAddress       uintptr
	AllocationBase    uintptr
	AllocationProtect uint32
	_                 uint32
	RegionSize        uintptr
	State             uint32
	Protect           uint32
	Type              uint32
	_                 uint32
}

const (
	memCommit      = 0x1000
	pageGuard      = 0x100
	pageNoAccess   = 0x01
	pageReadable   = 0x02 | 0x04 | 0x20 | 0x40 // R, RW, XR, XRW
)

func main() {
	if len(os.Args) < 3 {
		fmt.Println("usage: hlmem <pid> search|dump|regions ...")
		os.Exit(2)
	}
	pid64, _ := strconv.ParseUint(os.Args[1], 10, 32)
	cmd := os.Args[2]

	h, err := windows.OpenProcess(windows.PROCESS_VM_READ|windows.PROCESS_QUERY_INFORMATION, false, uint32(pid64))
	if err != nil {
		fmt.Println("OpenProcess failed (관리자 권한이 필요할 수 있음):", err)
		os.Exit(1)
	}
	defer windows.CloseHandle(h)

	switch cmd {
	case "roster":
		windows.CloseHandle(h)
		showRoster(uint32(pid64))
		return
	case "ingame":
		windows.CloseHandle(h)
		showInGame(uint32(pid64))
		return
	case "ipscan":
		ipscan(h)
	case "events":
		events(h)
	case "players":
		players(h)
	case "regions":
		listRegions(h)
	case "locate":
		if len(os.Args) < 4 {
			fmt.Println("usage: hlmem <pid> locate <text>")
			os.Exit(2)
		}
		locate(h, os.Args[3])
	case "cluster":
		if len(os.Args) < 5 {
			fmt.Println("usage: hlmem <pid> cluster <name1> <name2> [name3...]")
			os.Exit(2)
		}
		cluster(h, os.Args[3:])
	case "search":
		if len(os.Args) < 4 {
			fmt.Println("usage: hlmem <pid> search <text>")
			os.Exit(2)
		}
		search(h, os.Args[3])
	case "dump":
		if len(os.Args) < 5 {
			fmt.Println("usage: hlmem <pid> dump <hexaddr> <size>")
			os.Exit(2)
		}
		addr, _ := strconv.ParseUint(strings.TrimPrefix(os.Args[3], "0x"), 16, 64)
		size, _ := strconv.Atoi(os.Args[4])
		dump(h, uintptr(addr), size)
	default:
		fmt.Println("unknown command:", cmd)
	}
}

func virtualQuery(h windows.Handle, addr uintptr) (memBasicInfo, bool) {
	var mbi memBasicInfo
	r, _, _ := procVirtualQuery.Call(uintptr(h), addr,
		uintptr(unsafe.Pointer(&mbi)), unsafe.Sizeof(mbi))
	return mbi, r != 0
}

// iterateRegions calls fn for each committed, readable, non-guard region.
func iterateRegions(h windows.Handle, fn func(base uintptr, size uintptr)) {
	var addr uintptr
	for {
		mbi, ok := virtualQuery(h, addr)
		if !ok {
			break
		}
		if mbi.RegionSize == 0 {
			break
		}
		if mbi.State == memCommit && mbi.Protect&pageGuard == 0 &&
			mbi.Protect&pageNoAccess == 0 && mbi.Protect&pageReadable != 0 {
			fn(mbi.BaseAddress, mbi.RegionSize)
		}
		next := mbi.BaseAddress + mbi.RegionSize
		if next <= addr {
			break
		}
		addr = next
	}
}

// players scans memory for player JSON blobs and extracts name + battleTag.
func players(h windows.Handle) {
	needle := []byte(`"battleTag":"`)
	nameRe := regexp.MustCompile(`"name":"([^"]{1,64})"`)
	tagRe := regexp.MustCompile(`"battleTag":"([^"]{1,80})"`)
	prettyRe := regexp.MustCompile(`"prettyBattleTag":"([^"]{0,80})"`)
	toonRe := regexp.MustCompile(`"legacyChatToonId":(\d{1,12})`)

	type player struct{ name, tag, pretty, toon string }
	seen := map[string]player{}

	iterateRegions(h, func(base, size uintptr) {
		const chunk = 1 << 20
		for off := uintptr(0); off < size; off += chunk {
			rd := chunk
			if size-off < uintptr(rd) {
				rd = int(size - off)
			}
			data := readMem(h, base+off, rd)
			if data == nil {
				continue
			}
			for _, idx := range indexAll(data, needle) {
				// Read a window around the hit that should hold one JSON object.
				lo := idx - 300
				if lo < 0 {
					lo = 0
				}
				hi := idx + 200
				if hi > len(data) {
					hi = len(data)
				}
				win := data[lo:hi]
				tm := tagRe.FindSubmatch(win)
				if tm == nil {
					continue
				}
				tag := string(tm[1])
				p := player{tag: tag}
				if nm := nameRe.FindSubmatch(win); nm != nil {
					p.name = string(nm[1])
				}
				if pm := prettyRe.FindSubmatch(win); pm != nil {
					p.pretty = string(pm[1])
				}
				if tn := toonRe.FindSubmatch(win); tn != nil {
					p.toon = string(tn[1])
				}
				seen[tag] = p
			}
		}
	})

	fmt.Printf("=== 방 참가자 후보: %d명 (배틀태그 기준 중복제거) ===\n", len(seen))
	for _, p := range seen {
		fmt.Printf("  name=%-20q battleTag=%-24q toonId=%s\n", p.name, p.tag, p.toon)
	}
}

// ipscan searches memory for sockaddr_in structures (AF_INET + port + IP) and
// prints public (non-private, non-Blizzard) IP:port pairs — candidate P2P peers.
func ipscan(h windows.Handle) {
	type hit struct {
		ip   string
		port int
		n    int
	}
	seen := map[string]*hit{}
	iterateRegions(h, func(base, size uintptr) {
		const chunk = 1 << 20
		for off := uintptr(0); off < size; off += chunk {
			rd := chunk
			if size-off < uintptr(rd) {
				rd = int(size - off)
			}
			data := readMem(h, base+off, rd)
			if data == nil {
				continue
			}
			for i := 0; i+8 <= len(data); i++ {
				// sockaddr_in: sin_family=AF_INET(0x0002 LE), sin_port(BE), sin_addr(4)
				if data[i] != 0x02 || data[i+1] != 0x00 {
					continue
				}
				port := int(data[i+2])<<8 | int(data[i+3])
				if port == 0 || port > 65535 {
					continue
				}
				a, b, c, d := data[i+4], data[i+5], data[i+6], data[i+7]
				if !isPublicIP(a, b, c, d) {
					continue
				}
				ip := fmt.Sprintf("%d.%d.%d.%d", a, b, c, d)
				key := ip
				if hh, ok := seen[key]; ok {
					hh.n++
				} else {
					seen[key] = &hit{ip: ip, port: port, n: 1}
				}
			}
		}
	})
	fmt.Printf("=== 게임 메모리 내 public IP:port (sockaddr_in, %d개) ===\n", len(seen))
	for _, hh := range seen {
		fmt.Printf("  %-16s :%-6d  (%d회)\n", hh.ip, hh.port, hh.n)
	}
}

func isPublicIP(a, b, c, d byte) bool {
	if a == 0 || a == 127 || a >= 224 {
		return false
	}
	if a == 10 {
		return false
	}
	if a == 172 && b >= 16 && b <= 31 {
		return false
	}
	if a == 192 && b == 168 {
		return false
	}
	if a == 169 && b == 254 {
		return false
	}
	// Blizzard/cloud ranges seen on 443 (auth), exclude the obvious ones.
	if a == 137 && b == 221 {
		return false // Blizzard
	}
	return true
}

// events enumerates JSON "endpoint" event types and flags which ones carry a
// battleTag nearby — to locate where a room participant's battleTag might live.
func events(h windows.Handle) {
	epRe := regexp.MustCompile(`"endpoint":"([A-Za-z0-9_]{1,40})"`)
	counts := map[string]int{}
	withTag := map[string]int{}
	iterateRegions(h, func(base, size uintptr) {
		const chunk = 1 << 20
		for off := uintptr(0); off < size; off += chunk {
			rd := chunk
			if size-off < uintptr(rd) {
				rd = int(size - off)
			}
			data := readMem(h, base+off, rd)
			if data == nil {
				continue
			}
			for _, m := range epRe.FindAllSubmatchIndex(data, -1) {
				name := string(data[m[2]:m[3]])
				counts[name]++
				hi := m[1] + 600
				if hi > len(data) {
					hi = len(data)
				}
				if indexBytes(data[m[0]:hi], []byte(`"battleTag":"`)) >= 0 {
					withTag[name]++
				}
			}
		}
	})
	fmt.Printf("=== 로비/게임 이벤트 종류 (%d종) ===\n", len(counts))
	type kv struct {
		name       string
		n, tag     int
	}
	var arr []kv
	for k, v := range counts {
		arr = append(arr, kv{k, v, withTag[k]})
	}
	sort.Slice(arr, func(i, j int) bool { return arr[i].n > arr[j].n })
	for _, e := range arr {
		flag := ""
		if e.tag > 0 {
			flag = fmt.Sprintf("  ← battleTag 포함(%d)", e.tag)
		}
		fmt.Printf("  %-30s %6d회%s\n", e.name, e.n, flag)
	}
}

func showRoster(pid uint32) {
	store, _ := identity.Load(identityPath())
	parts, _, err := roster.Read(pid, store)
	_ = store.Save()
	if err != nil {
		fmt.Println("roster read failed:", err)
		return
	}
	fmt.Printf("=== 현재 방 참가자: %d명 ===\n", len(parts))
	for _, p := range parts {
		fmt.Printf("  슬롯%d  %-18s  핑=%-4dms  %-8s  battleTag=%-22s toonId=%s\n",
			p.SlotID, p.Name, p.Latency, p.Race, p.BattleTag, p.ToonID)
	}
}

// locate reports, for every committed readable region that contains needle,
// its Type (Private/Mapped/Image) and size — so we can see which filter drops it.
func locate(h windows.Handle, text string) {
	needle := []byte(text)
	typeName := func(t uint32) string {
		switch t {
		case 0x20000:
			return "PRIVATE"
		case 0x40000:
			return "MAPPED"
		case 0x1000000:
			return "IMAGE"
		default:
			return fmt.Sprintf("0x%x", t)
		}
	}
	var addr uintptr
	total := 0
	for {
		var mbi memBasicInfo
		r, _, _ := procVirtualQuery.Call(uintptr(h), addr,
			uintptr(unsafe.Pointer(&mbi)), unsafe.Sizeof(mbi))
		if r == 0 || mbi.RegionSize == 0 {
			break
		}
		commit := mbi.State == memCommit && mbi.Protect&pageGuard == 0 &&
			mbi.Protect&pageNoAccess == 0 && mbi.Protect&pageReadable != 0
		if commit {
			const chunk = 1 << 20
			hits := 0
			for off := uintptr(0); off < mbi.RegionSize; off += chunk {
				rd := chunk
				if mbi.RegionSize-off < uintptr(rd) {
					rd = int(mbi.RegionSize - off)
				}
				data := readMem(h, mbi.BaseAddress+off, rd)
				if data == nil {
					continue
				}
				hits += len(indexAll(data, needle))
			}
			if hits > 0 {
				total += hits
				fmt.Printf("  base=%#012x  size=%8.2fMB  type=%-8s  hits=%d\n",
					mbi.BaseAddress, float64(mbi.RegionSize)/1024/1024, typeName(mbi.Type), hits)
			}
		}
		next := mbi.BaseAddress + mbi.RegionSize
		if next <= addr {
			break
		}
		addr = next
	}
	fmt.Printf("총 %d개 hit\n", total)
}

// cluster finds windows of memory where >=2 of the given names co-occur within
// 512 bytes — the in-game roster array keeps all player names close together.
func cluster(h windows.Handle, names []string) {
	needles := make([][]byte, len(names))
	for i, n := range names {
		needles[i] = []byte(n)
	}
	const win = 512
	type occ struct {
		addr uintptr
		name int
	}
	iterateRegions(h, func(base, size uintptr) {
		const chunk = 1 << 20
		for off := uintptr(0); off < size; off += chunk {
			rd := chunk
			if size-off < uintptr(rd) {
				rd = int(size - off)
			}
			data := readMem(h, base+off, rd)
			if data == nil {
				continue
			}
			var occs []occ
			for ni, nd := range needles {
				for _, idx := range indexAll(data, nd) {
					occs = append(occs, occ{addr: base + off + uintptr(idx), name: ni})
				}
			}
			sort.Slice(occs, func(i, j int) bool { return occs[i].addr < occs[j].addr })
			for i := 0; i < len(occs); i++ {
				distinct := map[int]bool{occs[i].name: true}
				j := i + 1
				for j < len(occs) && occs[j].addr-occs[i].addr <= win {
					distinct[occs[j].name] = true
					j++
				}
				if len(distinct) >= 2 {
					fmt.Printf("  cluster @ %#012x  (%d distinct names in %d bytes):\n",
						occs[i].addr, len(distinct), win)
					for k := i; k < j; k++ {
						fmt.Printf("      +%-4d %s\n", occs[k].addr-occs[i].addr, names[occs[k].name])
					}
					i = j - 1
				}
			}
		}
	})
}

func showInGame(pid uint32) {
	store, _ := identity.Load(identityPath())
	parts, err := roster.InGamePlayers(pid, store)
	if err != nil {
		fmt.Println("ingame read failed:", err)
		return
	}
	fmt.Printf("=== 인게임 플레이어 배열: %d명 ===\n", len(parts))
	for _, p := range parts {
		fmt.Printf("  슬롯%d  %-18s  %-8s  battleTag=%s\n", p.SlotID, p.Name, p.Race, p.BattleTag)
	}
}

func listRegions(h windows.Handle) {
	var total uintptr
	n := 0
	iterateRegions(h, func(base, size uintptr) {
		total += size
		n++
		if n <= 40 {
			fmt.Printf("  base=%#012x size=%d\n", base, size)
		}
	})
	fmt.Printf("committed readable regions: %d, total %.1f MB\n", n, float64(total)/1024/1024)
}

func readMem(h windows.Handle, addr uintptr, size int) []byte {
	buf := make([]byte, size)
	var n uintptr
	err := windows.ReadProcessMemory(h, addr, &buf[0], uintptr(size), &n)
	if err != nil || n == 0 {
		return nil
	}
	return buf[:n]
}

func search(h windows.Handle, text string) {
	ascii := []byte(text)
	utf16 := utf16le(text)
	fmt.Printf("searching for %q (ascii %d bytes, utf16 %d bytes)\n", text, len(ascii), len(utf16))

	hitsA, hitsW := 0, 0
	iterateRegions(h, func(base, size uintptr) {
		// Read in chunks to bound memory use.
		const chunk = 1 << 20
		for off := uintptr(0); off < size; off += chunk {
			rd := chunk
			if size-off < uintptr(rd) {
				rd = int(size - off)
			}
			data := readMem(h, base+off, rd)
			if data == nil {
				continue
			}
			for _, idx := range indexAll(data, ascii) {
				fmt.Printf("  [ASCII] %#012x\n", base+off+uintptr(idx))
				hitsA++
			}
			for _, idx := range indexAll(data, utf16) {
				fmt.Printf("  [UTF16] %#012x\n", base+off+uintptr(idx))
				hitsW++
			}
		}
	})
	fmt.Printf("done: %d ASCII, %d UTF-16 hits\n", hitsA, hitsW)
}

func dump(h windows.Handle, addr uintptr, size int) {
	if size <= 0 || size > 4096 {
		size = 256
	}
	data := readMem(h, addr, size)
	if data == nil {
		fmt.Println("read failed at", fmt.Sprintf("%#x", addr))
		return
	}
	for i := 0; i < len(data); i += 16 {
		end := i + 16
		if end > len(data) {
			end = len(data)
		}
		row := data[i:end]
		hexParts := make([]string, 0, 16)
		for _, b := range row {
			hexParts = append(hexParts, fmt.Sprintf("%02x", b))
		}
		asc := make([]byte, 0, 16)
		for _, b := range row {
			if b >= 32 && b < 127 {
				asc = append(asc, b)
			} else {
				asc = append(asc, '.')
			}
		}
		fmt.Printf("  %#012x  %-48s  %s\n", addr+uintptr(i), strings.Join(hexParts, " "), string(asc))
	}
}

func indexAll(hay, needle []byte) []int {
	if len(needle) == 0 {
		return nil
	}
	var out []int
	start := 0
	for {
		i := indexBytes(hay[start:], needle)
		if i < 0 {
			break
		}
		out = append(out, start+i)
		start += i + 1
	}
	return out
}

func indexBytes(hay, needle []byte) int {
	n, m := len(hay), len(needle)
	if m == 0 || m > n {
		return -1
	}
	for i := 0; i+m <= n; i++ {
		if hay[i] == needle[0] {
			match := true
			for j := 1; j < m; j++ {
				if hay[i+j] != needle[j] {
					match = false
					break
				}
			}
			if match {
				return i
			}
		}
	}
	return -1
}

func utf16le(s string) []byte {
	u := windows.StringToUTF16(s)
	// drop trailing NUL
	if len(u) > 0 && u[len(u)-1] == 0 {
		u = u[:len(u)-1]
	}
	b := make([]byte, len(u)*2)
	for i, r := range u {
		binary.LittleEndian.PutUint16(b[i*2:], r)
	}
	return b
}
