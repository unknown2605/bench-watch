package collect

import "strings"

// letterPairs[a][b] is log2 of 676 times the share of "ab" among letter pairs in macOS /usr/share/dict/words,
// rounded, clipped to -6..3 and stored plus 6: random letters average below zero, words above (CM-00009 R2-1).
var letterPairs = [26]string{
	"18877574625979372989655454",
	"74237101720821610643610030",
	"80508008706602801737700060",
	"72148243820535710641523050",
	"85786664623879674998655642",
	"60006500600600600504600040",
	"71017155700645600731602050",
	"82118201800544810625503070",
	"86986773315879873799571307",
	"40004000200000400000400000",
	"52007103601423410232302030",
	"93459442903844850146742080",
	"86008200800254870030601060",
	"84889684835546853579644153",
	"66775574615889782988866543",
	"81108108700723850866602050",
	"00000000000000000000600000",
	"95669455915576961667654071",
	"73728327804675774289714060",
	"83519318900643820846704072",
	"66665450603879460887020212",
	"60008000600000500000300010",
	"62026104601314600321100010",
	"40304001500000440004200040",
	"53554242400655560565202202",
	"50006000400100500000100033",
}

// pairScore sums the letter-pair scores of a run of ASCII letters, whatever their case.
func pairScore(p string) int {
	sum := 0
	for i := 1; i < len(p); i++ {
		sum += int(letterPairs[(p[i-1]|0x20)-'a'][(p[i]|0x20)-'a']-'0') - 6
	}
	return sum
}

const (
	// Measured on program names and random letters (CM-00010): a run of whole words is judged from 7 pairs, a stretch
	// inside one part (glued words) from 11 and against a lower floor, which refuses fewer real compound names.
	minPairs   = 7
	pairFloor  = -5
	innerPairs = 11
	innerFloor = -8
	// An excuse stands beside at most 7 other letters, a word too short to judge alone, and against a higher floor;
	// past that the run is scored as spelled, so random letters gain nothing from an abbreviation (CM-00010 QA).
	maxRest     = 7
	liftedFloor = -3
)

// abbreviations are spellings program names glue to words (mysqldump, xpcproxy, newfs_msdos, vacuumdb); two-letter
// ones count only as a whole word or at the end of one.
var abbreviations = abbreviationIndex(`sql pdf jpg png gif html json xml http ftp tcp udp dns mdns snmp smtp rpc xpc usb
	gpu cpu gpg gnupg tls cgi dbm fsck newfs apfs hfs ntfs nfs msdos exfat util ctl cfg chk cmp pkg svc msg fmt txt tmp
	kext prefs passwd sync dylib sfx mdm mysql msedge fs db pg`)

func abbreviationIndex(text string) (index [26][]string) {
	for _, w := range strings.Fields(text) {
		index[w[0]-'a'] = append(index[w[0]-'a'], w)
	}
	return index
}

// pairRuns holds a name's letter-pair scores word by word: digits, separators and case changes end a word, so
// dictionary words beside a random part cannot offset it. lifted has the excused pairs raised to zero.
type pairRuns struct {
	raw    [maxNameLen]int8
	lifted [maxNameLen]int8
	ends   [maxNameLen]int8
	rest   [maxNameLen]int8
	n      int
	words  int
	glued  bool
}

func (r *pairRuns) add(p string) {
	camel, start, first := count(p, isLower) > 0, 0, r.n
	for i := 1; i <= len(p); i++ {
		if i < len(p) && sameWord(p, i, camel) {
			continue
		}
		r.word(p[start:i])
		start = i
	}
	r.glued = r.glued || lowStretch(r.lifted[first:r.n])
}

// lowStretch: some innerPairs or more pairs in a row sum below innerFloor.
func lowStretch(scores []int8) bool {
	sum, lag, best := 0, 0, 0
	for i, v := range scores {
		sum += int(v)
		if i+1 < innerPairs {
			continue
		}
		if i+1 > innerPairs {
			lag += int(scores[i-innerPairs])
			best = max(best, lag)
		}
		if sum-best < innerFloor {
			return true
		}
	}
	return false
}

// sameWord: with lowercase in the part a capital starts a word and the last capital of an acronym stands alone.
func sameWord(p string, i int, camel bool) bool {
	a, b := p[i-1], p[i]
	if isDigit(a) || isDigit(b) {
		return false
	}
	return !camel || (isLower(b) && !(isUpper(a) && i >= 2 && isUpper(p[i-2])))
}

func (r *pairRuns) word(w string) {
	first, n := r.n, len(w)
	if n < 2 || first+n-1 > len(r.raw) {
		return
	}
	for i := 1; i < n; i++ {
		v := int8(letterPairs[(w[i-1]|0x20)-'a'][(w[i]|0x20)-'a']) - '0' - 6
		r.raw[r.n], r.lifted[r.n] = v, v
		r.n++
	}
	lo, hi := 0, n
	// A daemon's closing d (cfprefsd, audiomxd) is not part of the word before it.
	if n >= 4 && w[n-1]|0x20 == 'd' {
		r.excuse(first, n-2, n-1)
		hi--
	}
	for matched := true; matched && hi-lo >= 2; {
		matched = false
		for _, a := range abbreviations[(w[lo]|0x20)-'a'] {
			if (len(a) >= 3 || n == 2) && foldPrefix(w[lo:hi], a) {
				r.excuse(first, max(lo-1, 0), min(lo+len(a), n-1))
				lo, matched = lo+len(a), true
				break
			}
		}
		for i := max(lo, hi-5); i < hi-1 && !matched; i++ {
			for _, a := range abbreviations[(w[i]|0x20)-'a'] {
				if len(a) == hi-i && foldPrefix(w[i:hi], a) {
					r.excuse(first, max(i-1, 0), min(hi, n-1))
					hi, matched = i, true
					break
				}
			}
		}
	}
	r.ends[r.words], r.rest[r.words] = int8(r.n), int8(hi-lo)
	r.words++
}

// excuse lifts the word's pairs from..to-1 to zero: an abbreviation and the pairs joining it to its neighbours.
func (r *pairRuns) excuse(first, from, to int) {
	for j := first + from; j < first+to; j++ {
		r.lifted[j] = max(r.lifted[j], 0)
	}
}

func foldPrefix(w, lower string) bool {
	if len(w) < len(lower) {
		return false
	}
	for i := 0; i < len(lower); i++ {
		if w[i]|0x20 != lower[i] {
			return false
		}
	}
	return true
}

// random: some run of whole words, or a stretch inside one part, scores below its floor.
func (r *pairRuns) random() bool {
	from := 0
	for a := 0; a < r.words; a++ {
		raw, lifted, rest, at := 0, 0, 0, from
		for b := a; b < r.words; b++ {
			for ; at < int(r.ends[b]); at++ {
				raw += int(r.raw[at])
				lifted += int(r.lifted[at])
			}
			if rest += int(r.rest[b]); at-from < minPairs {
				continue
			}
			if raw < pairFloor && (rest > maxRest || lifted < liftedFloor) {
				return true
			}
		}
		from = int(r.ends[a])
	}
	return r.glued
}
