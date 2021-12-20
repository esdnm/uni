//go:build generate
// +build generate

package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"zgo.at/uni/v2/unidata"
	"zgo.at/zli"
)

func main() {
	var err error
	if len(os.Args) > 1 {
		err = run(os.Args[1])
		zli.F(err)
		return
	}

	zli.F(run("codepoints"))
	zli.F(run("emojis"))
}

func run(which string) error {
	switch which {
	case "codepoints":
		return mkcodepoints()
	case "emojis":
		return mkemojis()
	default:
		return fmt.Errorf("unknown file: %q\n", which)
	}
}

func write(fp io.Writer, s string, args ...interface{}) {
	_, err := fmt.Fprintf(fp, s, args...)
	zli.F(err)
}

func readCLDR() map[string][]string {
	d, err := fetch("https://raw.githubusercontent.com/unicode-org/cldr/master/common/annotations/en.xml")
	zli.F(err)

	var cldr struct {
		Annotations []struct {
			CP    string `xml:"cp,attr"`
			Type  string `xml:"type,attr"`
			Names string `xml:",innerxml"`
		} `xml:"annotations>annotation"`
	}
	zli.F(xml.Unmarshal(d, &cldr))

	out := make(map[string][]string)
	for _, a := range cldr.Annotations {
		if a.Type != "tts" {
			out[a.CP] = strings.Split(a.Names, " | ")
		}
	}
	return out
}

func mkemojis() error {
	text, err := fetch("https://unicode.org/Public/emoji/14.0/emoji-test.txt")
	zli.F(err)

	cldr := readCLDR()

	fp, err := os.Create("gen_emojis.go")
	zli.F(err)
	defer func() { zli.F(fp.Close()) }()

	write(fp, "// Code generated by gen.go; DO NOT EDIT\n\n"+
		"package unidata\n\n")

	var (
		emojis          = make(map[string][]string)
		order           []string
		group, subgroup string
		groups          []string
		subgroups       = make(map[string][]string)
	)
	for _, line := range strings.Split(string(text), "\n") {
		// Groups are listed as a comment, but we want to preserve them.
		// # group: Smileys & Emotion
		// # subgroup: face-smiling
		if strings.HasPrefix(line, "# group: ") {
			group = line[strings.Index(line, ":")+2:]
			groups = append(groups, group)
			continue
		}
		if strings.HasPrefix(line, "# subgroup: ") {
			subgroup = line[strings.Index(line, ":")+2:]
			subgroups[group] = append(subgroups[group], subgroup)
			continue
		}

		var comment string
		if p := strings.Index(line, "#"); p > -1 {
			comment = strings.TrimSpace(line[p+1:])
			line = strings.TrimSpace(line[:p])
		}
		if len(line) == 0 {
			continue
		}

		// "only fully-qualified emoji zwj sequences should be generated by
		// keyboards and other user input devices"
		if !strings.HasSuffix(line, "; fully-qualified") {
			continue
		}

		codepoints := strings.TrimSpace(strings.Split(line, ";")[0])

		// Get the name from the comment:
		//   # 😀 E2.0 grinning face
		//   # 🦶🏿 E11.0 foot: dark skin tone
		name := strings.SplitN(comment, " ", 3)[2]

		const (
			GenderNone = 0
			GenderSign = 1
			GenderRole = 2
		)

		tone := false
		gender := GenderNone
		var cp []string
		splitCodepoints := strings.Split(codepoints, " ")
		for i, c := range splitCodepoints {
			d, err := strconv.ParseInt(string(c), 16, 64)
			if err != nil {
				return err
			}

			switch d {
			// Skin tones
			case 0x1f3fb, 0x1f3fc, 0x1f3fd, 0x1f3fe, 0x1f3ff:
				tone = true
			// ZWJ
			case 0x200d:
				// No nothing

			// Old/classic gendered emoji. A "person" emoji is combined with "female
			// sign" or "male sign" to make an explicitly gendered one:
			//
			//   1F937                 # 🤷 E4.0 person shrugging
			//   1F937 200D 2642 FE0F  # 🤷‍♂️ E4.0 man shrugging
			//   1F937 200D 2640 FE0F  # 🤷‍♀️ E4.0 woman shrugging
			//
			//   2640                  # ♀ E4.0 female sign
			//   2642                  # ♂ E4.0 male sign
			//
			// Detect: 2640 or 2642 occurs in sequence position>0 to exclude just
			// the female/male signs.
			case 0x2640, 0x2642:
				if i == 0 {
					cp = append(cp, fmt.Sprintf("0x%x", d))
				} else {
					gender = GenderSign
				}
			default:
				cp = append(cp, fmt.Sprintf("0x%x", d))
			}
		}

		// This ignores combining the "holding hands", "handshake", and
		// "kissing" with different skin tone variants, where you can select a
		// different tone for each side (i.e. hand or person):
		//
		//   1F468 1F3FB 200D 1F91D 200D 1F468 1F3FF 👨🏻‍🤝‍👨🏿
		//   E12.1 men holding hands: light skin tone, dark skin tone
		//
		//   1F9D1 1F3FB 200D 2764 FE0F 200D 1F48B 200D 1F9D1 1F3FF 🧑🏻‍❤️‍💋‍🧑🏿
		//   E13.1 kiss: person, person, light skin tone, dark skin tone
		//
		// There is no good way to select this with the current UX/flagset; and
		// to be honest I don't think it's very important either, so just skip
		// it for now.
		//
		// TODO: I guess the best way to fix this is to allow multiple values
		// for -t and -g:
		//
		//   uni e handshake -t dark            Both hands dark
		//   uni e handshake -t dark -t light   Left hand dark, right hand light
		//
		// Actually, I'd change it and make multiple -t and -g flags print
		// multiple variants (like "-t light,dark" does now), and then change
		// the meaning of "-t light,dark" to the above to select multiple
		// variants in the same emoji. That makes more sense, but is not a
		// backwards-compatible change. Guess we can do it for uni 3.0.
		if tone && (strings.Contains(name, "holding hands") || strings.Contains(name, "handshake")) {
			gender = 0
			tone = false
			continue
		}
		if tone && (strings.Contains(name, "kiss:") || strings.Contains(name, "couple with heart")) {
			tone = false
			continue
		}

		key := strings.Join(cp, ", ")

		// Newer gendered emoji; combine "person", "man", or "women" with
		// something related to that:
		//
		//   1F9D1 200D 2695 FE0F # 🧑‍⚕️ E12.1 health worker
		//   1F468 200D 2695 FE0F # 👨‍⚕️ E4.0 man health worker
		//   1F469 200D 2695 FE0F # 👩‍⚕️ E4.0 woman health worker
		//
		//   1F9D1                # 🧑 E5.0 person
		//   1F468                # 👨 E2.0 man
		//   1F469                # 👩 E2.0 woman
		//
		// Detect: These only appear in the person-role and person-activity
		// subgroups; the special cases only in family subgroup.
		for _, g := range gendered {
			if strings.HasPrefix(key, g) {
				gender = GenderRole
			}
		}

		if gender == GenderRole {
			key = strings.Join(append([]string{"0x1f9d1"}, cp[1:]...), ", ")
			_, ok := emojis[key]
			if !ok {
				return fmt.Errorf("not found: %q %q", key, name)
			}

			emojis[key][5] = fmt.Sprintf("%d", gender)
			continue
		}

		if gender == GenderSign {
			_, ok := emojis[key]
			if !ok && cp[len(cp)-1] == "0xfe0f" {
				key = strings.Join(cp[0:len(cp)-1], ", ")
			}
			_, ok = emojis[key]
			if !ok {
				return fmt.Errorf("not found: %q %q", key, name)
			}

			emojis[key][5] = fmt.Sprintf("%d", gender)
			continue
		}

		if tone {
			_, ok := emojis[key]
			if !ok && cp[len(cp)-1] == "0xfe0f" {
				key = strings.Join(cp[0:len(cp)-1], ", ")
			} else if !ok {
				key = strings.Join(append(cp, "0xfe0f"), ", ")
			}
			_, ok = emojis[key]
			if !ok {
				return fmt.Errorf("not found: %q %q", key, name)
			}

			emojis[key][4] = "true"
			continue
		}

		emojis[key] = []string{
			strings.Join(cp, ", "), name, group, subgroup, "false", "0"}
		order = append(order, key)
	}

	// We should really parse it like this in the above loop, but I don't feel
	// like rewriting all of this, and this makes adding cldr easier.
	emo := make([]unidata.Emoji, len(order))
	for i, k := range order {
		e := emojis[k]

		g, _ := strconv.Atoi(e[5])
		var cp []rune
		for _, c := range strings.Split(e[0], ", ") {
			n, err := strconv.ParseUint(c[2:], 16, 32)
			zli.F(err)
			cp = append(cp, rune(n))
		}

		var groupID, subgroupID int
		for i, g := range groups {
			if g == e[2] {
				groupID = i
				break
			}
		}
		for i, g := range subgroups[e[2]] {
			if g == e[3] {
				subgroupID = i
				break
			}
		}

		emo[i] = unidata.Emoji{
			Codepoints: cp,
			Name:       e[1],
			Group:      groupID,
			Subgroup:   subgroupID,
			SkinTones:  e[4] == "true",
			Genders:    g,
		}
		emo[i].CLDR = cldr[strings.ReplaceAll(strings.ReplaceAll(emo[i].String(), "\ufe0f", ""), "\ufe0e", "")]
	}

	write(fp, "var EmojiGroups = []string{\n")
	for _, g := range groups {
		write(fp, "\t%#v,\n", g)
	}
	write(fp, "}\n\n")

	write(fp, "var EmojiSubgroups = map[string][]string{\n")
	for _, g := range groups {
		write(fp, "\t%#v: []string{\n", g)
		for _, sg := range subgroups[g] {
			write(fp, "\t\t%#v,\n", sg)
		}
		write(fp, "\t},\n")
	}
	write(fp, "}\n\n")

	write(fp, "var Emojis = []Emoji{\n")
	for _, e := range emo {
		var cp string
		for _, c := range e.Codepoints {
			cp += fmt.Sprintf("0x%x, ", c)
		}
		cp = cp[:len(cp)-2]

		//                   CP   Name Grp  Sgr  CLDR sk  gnd
		write(fp, "\t{[]rune{%s}, %#v, %#v, %#v, %#v, %t, %d},\n",
			cp, e.Name, e.Group, e.Subgroup, e.CLDR, e.SkinTones, e.Genders)
	}
	write(fp, "}\n\n")

	return nil
}

// TODO: add casefolding
// https://unicode.org/Public/13.0.0/ucd/CaseFolding.txt
// CaseFold []rune

// TODO: add properties:
// https://unicode.org/Public/13.0.0/ucd/PropList.txt
// "uni p dash" should print all dashes.
//
//
// TODO: add "confusable" information from
// https://www.unicode.org/Public/idna/13.0.0/
// and/or
// https://www.unicode.org/Public/security/13.0.0/
//
//
// TODO: add "alias" information from
// https://unicode.org/Public/13.0.0/ucd/NamesList.txt
// This is generated from other sources, but I can't really find where it gts
// that "x (modifier letter prime - 02B9)" from.
//
// 0027	APOSTROPHE
// 	= apostrophe-quote (1.0)
// 	= APL quote
// 	* neutral (vertical) glyph with mixed usage
// 	* 2019 is preferred for apostrophe
// 	* preferred characters in English for paired quotation marks are 2018 & 2019
// 	* 05F3 is preferred for geresh when writing Hebrew
// 	x (modifier letter prime - 02B9)
// 	x (modifier letter apostrophe - 02BC)
// 	x (modifier letter vertical line - 02C8)
// 	x (combining acute accent - 0301)
// 	x (hebrew punctuation geresh - 05F3)
// 	x (prime - 2032)
// 	x (latin small letter saltillo - A78C)

// http://www.unicode.org/reports/tr44/
func mkcodepoints() error {
	text, err := fetch("https://www.unicode.org/Public/UCD/latest/ucd/UnicodeData.txt")
	zli.F(err)

	var (
		widths   = loadwidths()
		entities = loadentities()
		digraphs = loaddigraphs()
		keysyms  = loadkeysyms()
	)

	fp, err := os.Create("gen_codepoints.go")
	zli.F(err)
	defer func() { zli.F(fp.Close()) }()

	write(fp, "// Code generated by gen.go; DO NOT EDIT\n\n"+
		"package unidata\n\n"+
		"var Codepoints = map[rune]Codepoint{\n")

	for _, line := range bytes.Split(text, []byte("\n")) {
		if p := bytes.Index(line, []byte("#")); p > -1 {
			line = bytes.TrimSpace(line[:p])
		}
		if len(line) == 0 {
			continue
		}

		s := bytes.Split(line, []byte(";"))
		// Some properties (most notably control characters) all have the name
		// as <control>, which isn't very useful. The old (obsolete) Unicode 1
		// name field has a more useful name.
		// TODO: add this information from:
		// https://www.unicode.org/Public/UCD/latest/ucd/NamesList.txt
		name := s[1]
		if name[0] == '<' && len(s[10]) > 1 {
			name = s[10]
		}

		c, err := strconv.ParseUint(string(s[0]), 16, 32)
		zli.F(err)
		cp := rune(c)

		entitiy := entities[cp]
		digraph := digraphs[cp]
		keysym := ""
		if _, ok := keysyms[cp]; ok {
			keysym = keysyms[cp][0]
		}

		//             CP     Wid    Cat Name Vim HTML Enti KSym
		write(fp, "\t0x%x: {0x%[1]x, %d, %d, %#v, %#v, %#v, %#v},\n",
			cp, widths[cp], unidata.Catmap[string(s[2])], string(name), digraph, entitiy, keysym)
	}

	write(fp, "}\n")
	return nil
}

func loadwidths() map[rune]uint8 {
	text, err := fetch("http://www.unicode.org/Public/UCD/latest/ucd/EastAsianWidth.txt")
	zli.F(err)

	widths := make(map[rune]uint8)
	for _, line := range bytes.Split(text, []byte("\n")) {
		if p := bytes.Index(line, []byte("#")); p > -1 {
			line = bytes.TrimSpace(line[:p])
		}
		if len(line) == 0 {
			continue
		}

		s := bytes.Split(line, []byte(";"))
		width := getwidth(string(s[1]))

		// Single codepoint.
		if !bytes.Contains(s[0], []byte("..")) {
			cp, err := strconv.ParseUint(string(s[0]), 16, 32)
			zli.F(err)

			widths[rune(cp)] = width
			continue
		}

		rng := bytes.Split(s[0], []byte(".."))
		start, err := strconv.ParseUint(string(rng[0]), 16, 32)
		zli.F(err)

		end, err := strconv.ParseUint(string(rng[1]), 16, 32)
		zli.F(err)

		for cp := start; end >= cp; cp++ {
			widths[rune(cp)] = width
		}
	}

	return widths
}

func getwidth(w string) uint8 {
	switch w {
	case "A":
		return unidata.WidthAmbiguous
	case "F":
		return unidata.WidthFullWidth
	case "H":
		return unidata.WidthHalfWidth
	case "N":
		return unidata.WidthNarrow
	case "Na":
		return unidata.WidthNeutral
	case "W":
		return unidata.WidthWide
	default:
		panic("wtf") // Never happens
	}
}

func loadentities() map[rune]string {
	j, err := fetch("https://html.spec.whatwg.org/entities.json")
	zli.F(err)

	var out map[string]struct {
		Codepoints []rune `json:"codepoints"`
	}
	zli.F(json.Unmarshal(j, &out))

	sorted := []string{}
	for k, _ := range out {
		// Don't need backwards-compatible versions without closing ;
		if !strings.HasSuffix(k, ";") {
			continue
		}

		sorted = append(sorted, k)
	}

	// Sort by name first, in reverse order. This way &quot; will be prefered
	// over &QUOT. Then sort by length, so we have the shortest (&nbsp; instead
	// of &NonBreakingSpace;).
	sort.Strings(sorted)
	for i, j := 0, len(sorted)-1; i < j; i, j = i+1, j-1 {
		sorted[i], sorted[j] = sorted[j], sorted[i]
	}
	sort.Slice(sorted, func(i, j int) bool {
		return len(sorted[i]) < len(sorted[j])
	})

	entities := make(map[rune]string)
	var seen []rune
	for _, ent := range sorted {
		cp := out[ent].Codepoints

		// TODO: some entities represent two codepoints; for example
		// &NotEqualTilde; is U+02242 (MINUS TILDE) plus U+000338 (COMBINING
		// LONG SOLIDUS OVERLAY).
		// I can't be bothered to implement this right now.
		if len(cp) != 1 {
			continue
		}

		found := false
		for _, s := range seen {
			if cp[0] == s {
				found = true
				break
			}
		}
		if found {
			continue
		}

		entities[cp[0]] = strings.Trim(ent, "&;")

		// TODO: don't need seen?
		seen = append(seen, cp[0])
	}

	return entities
}

func loadkeysyms() map[rune][]string {
	header, err := fetch("https://gitlab.freedesktop.org/xorg/proto/xorgproto/-/raw/master/include/X11/keysymdef.h")
	zli.F(err)

	ks := make(map[rune][]string)
	for _, line := range strings.Split(string(header), "\n") {
		if !strings.HasPrefix(line, "#define XK") {
			continue
		}

		sp := strings.Fields(line)
		if len(sp) < 4 {
			continue
		}
		cp, err := strconv.ParseInt(strings.TrimPrefix(sp[4], "U+"), 16, 32)
		if err != nil {
			continue
		}

		ks[rune(cp)] = append(ks[rune(cp)], strings.TrimPrefix(sp[1], "XK_"))
	}

	return ks
}

func loaddigraphs() map[rune]string {
	data, err := fetch("https://tools.ietf.org/rfc/rfc1345.txt")
	zli.F(err)

	re := regexp.MustCompile(`^ .*?   +[0-9a-f]{4}`)

	dg := make(map[rune]string)
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, "ISO-IR-") {
			continue
		}

		if !re.MatchString(line) {
			continue
		}

		// EG     0097    END OF GUARDED AREA (EPA)
		sp := strings.Fields(line)
		cp, err := strconv.ParseInt(strings.TrimPrefix(sp[1], "U+"), 16, 32)
		if err != nil {
			continue
		}
		dg[rune(cp)] = sp[0]
	}

	// Not in the RFC but in Vim, so add manually.
	dg[0x20ac] = "=e" // € (Euro)
	dg[0x20bd] = "=R" // ₽ (Ruble); also =P and the only one with more than one digraph :-/
	return dg
}

// Load .cache/file if it exists, or fetch from URL and store in .cache if it
// doesn't.
func fetch(url string) ([]byte, error) {
	file := "./.cache/" + path.Base(url)
	if _, err := os.Stat(file); err == nil {
		return ioutil.ReadFile(file)
	}

	err := os.MkdirAll("./.cache", 0777)
	if err != nil {
		return nil, fmt.Errorf("cannot create cache directory: %s", err)
	}

	client := http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("cannot download %q: %s", url, err)
	}
	defer resp.Body.Close()

	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cannot read body of %q: %s", url, err)
	}

	if resp.StatusCode != http.StatusOK {
		return data, fmt.Errorf("unexpected status code %d %s for %q",
			resp.StatusCode, resp.Status, url)
	}

	err = ioutil.WriteFile(file, data, 0666)
	if err != nil {
		return nil, fmt.Errorf("could not write cache: %s", err)
	}

	return data, nil
}

var gendered = []string{
	"0x1f468, 0x2695, 0xfe0f",
	"0x1f468, 0x1f393",
	"0x1f468, 0x1f3eb",
	"0x1f468, 0x2696, 0xfe0f",
	"0x1f468, 0x1f33e",
	"0x1f468, 0x1f373",
	"0x1f468, 0x1f527",
	"0x1f468, 0x1f3ed",
	"0x1f468, 0x1f4bc",
	"0x1f468, 0x1f52c",
	"0x1f468, 0x1f4bb",
	"0x1f468, 0x1f3a4",
	"0x1f468, 0x1f3a8",
	"0x1f468, 0x2708, 0xfe0f",
	"0x1f468, 0x1f680",
	"0x1f468, 0x1f692",
	"0x1f468, 0x1f9af",
	"0x1f468, 0x1f9bc",
	"0x1f468, 0x1f9bd",
	"0x1f469, 0x2695, 0xfe0f",
	"0x1f469, 0x1f393",
	"0x1f469, 0x1f3eb",
	"0x1f469, 0x2696, 0xfe0f",
	"0x1f469, 0x1f33e",
	"0x1f469, 0x1f373",
	"0x1f469, 0x1f527",
	"0x1f469, 0x1f3ed",
	"0x1f469, 0x1f4bc",
	"0x1f469, 0x1f52c",
	"0x1f469, 0x1f4bb",
	"0x1f469, 0x1f3a4",
	"0x1f469, 0x1f3a8",
	"0x1f469, 0x2708, 0xfe0f",
	"0x1f469, 0x1f680",
	"0x1f469, 0x1f692",
	"0x1f469, 0x1f9af",
	"0x1f469, 0x1f9bc",
	"0x1f469, 0x1f9bd",
}
