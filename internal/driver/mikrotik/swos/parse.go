package swos

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// L'analyseur des pages `*.b` de SwOS — le socle de tout le connecteur.
//
// SwOS ne sert pas de JSON. Il sert des litteraux JavaScript, tels que son
// interface les mange dans un `eval()` :
//
//	{upt:0x00e42cfe,mac:'b869f45f14a8',brd:'4353533332362d3234472d32532b',
//	 spd:[0x07,0x07,0x03],nm:['506f727431','53465031']}
//
// Trois ecarts avec JSON, et chacun casse un decoder standard :
//
//   - les keys n'ont PAS de guillemets ;
//   - les nombres sont hexadecimaux, prefixes `0x`, et de largeur variable — la
//     page vivante rembourre (`0x0063`), la sauvegarde ne rembourre pas (`0x63`) ;
//   - les strings sont entre apostrophes SIMPLES et leur contenu est de l'UTF-8
//     hex-encoded (`'506f727431'` = « Port1 »).
//
// Hence cet analyseur. Il est written a la main parce qu'il n'y a pas d'autre choix,
// et il est verified sur des captures reelles de the device (testdata/) parce
// que c'est la seule preuve qui vaille.
//
// LE PARTI PRIS CENTRAL : un champ absent est ABSENT, jamais zero.
//
// L'interface de the device fait l'inverse — son accesseur (`Ca` dans son
// JavaScript) rend la valeur par default declared dans la definition du champ
// quand la page ne porte pas la key. C'est acceptable pour dessiner un formulaire,
// et inacceptable ici : « ce port est dans le VLAN 0 » et « la page ne dit rien de
// ce port » menent a des ecritures opposees. Tous les accesseurs rendent donc un
// second retour booleen, et aucun ne fabrique de valeur.

// Kind est la nature d'une valeur lue sur une page.
type Kind int

const (
	// KindNum : un entier hexadecimal (`0x0063`).
	KindNum Kind = iota
	// KindStr : une string hex-encoded (`'506f727431'`). Le contenu reste BRUT
	// jusqu'a ce qu'on demande son texte : une string mal formee doit se signaler
	// au moment where on la lit, pas faire echouer la page entire.
	KindStr
	// KindList : un tableau, generally indexe par number de port.
	KindList
	// KindObj : un objet imbrique. Rare sur les pages, la forme normale de
	// /backup.swb.
	KindObj
)

// Value est une valeur lue sur une page `*.b`.
type Value struct {
	Kind Kind
	// Num porte les entiers. SwOS ne depasse jamais 32 bits sur une valeur
	// scalaire : ses masques de ports au-dela de 32 ports passent par un TABLEAU
	// de mots (cf. Bit).
	Num uint32
	// Raw porte les strings telles que servies — donc encore en hexadecimal.
	Raw  string
	List []Value
	Obj  *Page
}

// Page est un objet lu sur une page `*.b`, l'ordre des keys conserve.
//
// L'ordre n'est pas cosmetique : les ecritures renvoient a the device le jeu de
// champs complet de la page, et reproduire l'ordre dans lequel il l'a servi rend
// deux transcripts comparables ligne a ligne. Sur une plateforme where l'on ne peut
// pas relire ce qui a been envoye, c'est la seule way de comprendre un failure sans
// retourner sur site.
type Page struct {
	fields map[string]Value
	order  []string
}

// Keys rend les keys dans l'ordre where the device les a servies.
func (p *Page) Keys() []string {
	if p == nil {
		return nil
	}
	return append([]string{}, p.order...)
}

// Has dit si la page porte la key. A preferer a un accesseur dont on ignorerait
// le second retour.
func (p *Page) Has(key string) bool {
	if p == nil {
		return false
	}
	var _, ok = p.fields[key]
	return ok
}

// Value rend la valeur brute.
func (p *Page) Value(key string) (Value, bool) {
	if p == nil {
		return Value{}, false
	}
	var v, ok = p.fields[key]
	return v, ok
}

// Num rend un entier. `ok` est faux si la key manque OU si elle porte autre chose
// qu'un nombre — confondre les deux ferait lire un tableau comme un scalaire.
func (p *Page) Num(key string) (uint32, bool) {
	var v, ok = p.Value(key)
	if !ok || v.Kind != KindNum {
		return 0, false
	}
	return v.Num, true
}

// Text decodes une string hex-encoded.
//
// Le decoding reproduit celui de the device (`Ga` dans son JavaScript) :
// l'octet NUL TERMINE la string. Ces champs sont des tampons de taille fixe, et
// ce qui suit le NUL est du residu — le rendre ferait apparaitre des caracteres
// que l'exploitant ne voit nulle part dans l'interface de son switch.
//
// Le rembourrage par ESPACES, lui, est conserve : c'est ainsi que les champs
// SFF-8472 d'un transceiver sont written (`'46532020…'` = « FS » suivi de blancs),
// et c'est a l'appelant de decider s'il le rogne — pas a l'analyseur d'effacer une
// information qu'il a lue.
func (p *Page) Text(key string) (string, bool) {
	var v, ok = p.Value(key)
	if !ok || v.Kind != KindStr {
		return "", false
	}
	return decodeHexText(v.Raw)
}

// Nums rend un tableau d'entiers, typiquement indexe par port.
func (p *Page) Nums(key string) ([]uint32, bool) {
	var v, ok = p.Value(key)
	if !ok || v.Kind != KindList {
		return nil, false
	}
	var out = make([]uint32, 0, len(v.List))
	var e Value
	for _, e = range v.List {
		if e.Kind != KindNum {
			return nil, false
		}
		out = append(out, e.Num)
	}
	return out, true
}

// NumAt rend l'element d'index `i` d'un tableau d'entiers. Un index hors bornes
// n'est pas un zero : c'est une absence, et elle se dit.
func (p *Page) NumAt(key string, i int) (uint32, bool) {
	var list, ok = p.Nums(key)
	if !ok || i < 0 || i >= len(list) {
		return 0, false
	}
	return list[i], true
}

// Texts rend un tableau de strings decodees. Une seule string illisible fait
// echouer l'ensemble : rendre les autres en silence laisserait un trou a un index
// qu'on croirait vide.
func (p *Page) Texts(key string) ([]string, bool) {
	var v, ok = p.Value(key)
	if !ok || v.Kind != KindList {
		return nil, false
	}
	var out = make([]string, 0, len(v.List))
	var e Value
	for _, e = range v.List {
		if e.Kind != KindStr {
			return nil, false
		}
		var s string
		s, ok = decodeHexText(e.Raw)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// TextAt rend l'element d'index `i` d'un tableau de strings.
func (p *Page) TextAt(key string, i int) (string, bool) {
	var list, ok = p.Texts(key)
	if !ok || i < 0 || i >= len(list) {
		return "", false
	}
	return list[i], true
}

// Obj rend un objet imbrique (les valeurs de /backup.swb).
func (p *Page) Obj(key string) (*Page, bool) {
	var v, ok = p.Value(key)
	if !ok || v.Kind != KindObj || v.Obj == nil {
		return nil, false
	}
	return v.Obj, true
}

// List rend un tableau d'objets — la forme des tables (/vlan.b, /host.b, /acl.b).
func (p *Page) List(key string) ([]*Page, bool) {
	var v, ok = p.Value(key)
	if !ok || v.Kind != KindList {
		return nil, false
	}
	var out = make([]*Page, 0, len(v.List))
	var e Value
	for _, e = range v.List {
		if e.Kind != KindObj || e.Obj == nil {
			return nil, false
		}
		out = append(out, e.Obj)
	}
	return out, true
}

// Bit lit le bit de rang `i` d'un masque de ports.
//
// Deux formes coexistent sur cette plateforme, et se tromper de forme donne un
// masque de ports offset — i.e. une configuration applied sur les MAUVAIS
// ports :
//
//   - jusqu'a 32 ports, le masque est un SCALAIRE et le bit 0 est le port 1
//     (`fp1:0x03fffffe` sur un 26 ports : tous les ports sauf le premier) ;
//   - au-dela, c'est un TABLEAU de mots de 32 bits dont le DERNIER element porte
//     les bits 0 a 31, l'avant-dernier les bits 32 a 63.
//
// L'ordre des mots est observed dans le JavaScript de the device, qui parcourt le
// tableau a l'envers (`for(c=a.length-1; 0<=c; --c){ if(32>b) return a[c]>>b&1;
// b-=32 }`). Aucun model SwOS connu ne depasse 32 ports, mais coder l'ordre
// inverse par symetrie apparente aurait been un trap silencieux le jour where l'un
// en depasserait.
func (p *Page) Bit(key string, i int) (bool, bool) {
	if i < 0 {
		return false, false
	}
	var v, ok = p.Value(key)
	if !ok {
		return false, false
	}
	switch v.Kind {
	case KindNum:
		if i >= 32 {
			return false, false
		}
		return v.Num>>uint(i)&1 == 1, true
	case KindList:
		var words, listOK = p.Nums(key)
		if !listOK {
			return false, false
		}
		var word = len(words) - 1 - i/32
		if word < 0 {
			return false, false
		}
		return words[word]>>uint(i%32)&1 == 1, true
	}
	return false, false
}

// SetNum pose un entier. Utilise par les ecritures, qui relisent la page,
// modifient ce qu'elles doivent, et la renvoient entire.
func (p *Page) SetNum(key string, v uint32) {
	p.set(key, Value{Kind: KindNum, Num: v})
}

// SetText pose une string, hex-encoded comme the device l'attend.
func (p *Page) SetText(key string, s string) {
	p.set(key, Value{Kind: KindStr, Raw: hex.EncodeToString([]byte(s))})
}

// SetNums pose un tableau d'entiers.
func (p *Page) SetNums(key string, vs []uint32) {
	var list = make([]Value, 0, len(vs))
	var v uint32
	for _, v = range vs {
		list = append(list, Value{Kind: KindNum, Num: v})
	}
	p.set(key, Value{Kind: KindList, List: list})
}

// SetBit pose le bit de rang `i` d'un masque de ports, en conservant la FORME
// (scalaire ou tableau de mots) que the device a servie.
//
// Conserver la forme n'est pas un detail d'esthetique : renvoyer un scalaire there where
// the device attend un tableau, ou l'inverse, c'est renvoyer un masque qu'il ne
// lira pas comme on l'a written. Une key absente n'est PAS created — un masque
// fabrique de rien vaudrait « aucun port », et sur `allp` (« Allow From Ports »)
// cela suffit a rendre the device injoignable.
func (p *Page) SetBit(key string, i int, on bool) bool {
	if i < 0 {
		return false
	}
	var v, ok = p.Value(key)
	if !ok {
		return false
	}
	switch v.Kind {
	case KindNum:
		if i >= 32 {
			return false
		}
		var mask = uint32(1) << uint(i)
		if on {
			p.SetNum(key, v.Num|mask)
		} else {
			p.SetNum(key, v.Num&^mask)
		}
		return true
	case KindList:
		var words, listOK = p.Nums(key)
		if !listOK {
			return false
		}
		var word = len(words) - 1 - i/32
		if word < 0 {
			return false
		}
		var mask = uint32(1) << uint(i%32)
		if on {
			words[word] |= mask
		} else {
			words[word] &^= mask
		}
		p.SetNums(key, words)
		return true
	}
	return false
}

func (p *Page) set(key string, v Value) {
	if p.fields == nil {
		p.fields = map[string]Value{}
	}
	var _, seen = p.fields[key]
	if !seen {
		p.order = append(p.order, key)
	}
	p.fields[key] = v
}

// NewPage construit une page vide. Sert aux ecritures qui n'ont qu'un champ a
// poser (/!pwd.b), jamais a celles qui modifient une page existante — celles-there
// RELISENT (cf. apply.go).
func NewPage() *Page { return &Page{fields: map[string]Value{}} }

// Only rend une COPIE de la page reduite aux keys demandees, dans l'ordre requested.
//
// C'est l'outil du filtrage write, et il est central : une page vivante porte
// des champs en lecture seule (l'uptime, la temperature, la MAC) que the device
// n'attend pas en retour. On ne renvoie donc que le jeu de champs INSCRIPTIBLE de
// la page, observed dans sa propre sauvegarde de configuration (cf. pages.go).
//
// Une key demandee mais absente de la page lue est flagged, pas invented :
// writing doit s'stop there. Renvoyer une page truncated a un device qui
// attend le jeu complet, c'est lui laisser remettre a zero ce qu'on n'a pas dit.
func (p *Page) Only(keys []string) (*Page, []string) {
	var out = NewPage()
	var missing []string
	var k string
	for _, k = range keys {
		var v, ok = p.Value(k)
		if !ok {
			missing = append(missing, k)
			continue
		}
		out.set(k, v)
	}
	return out, missing
}

// Encode rend la page dans la syntaxe que the device attend en write.
//
// L'encodage des nombres reproduit EXACTEMENT celui de l'interface de
// the device (`Pa` dans son JavaScript) : hexadecimal, sans majuscules, rembourre
// a un nombre PAIR de chiffres — donc `0x00` pour zero, `0x63` pour 99,
// `0x03ffffff` pour un masque de 26 ports. Rien ne prouve que the device soit
// sensible a ce rembourrage ; reproduire a la lettre ce que son interface envoie
// est simplement la seule forme dont on sache qu'elle est accepted.
func (p *Page) Encode() string {
	var b strings.Builder
	b.WriteByte('{')
	var i int
	var k string
	for i, k = range p.order {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteByte(':')
		b.WriteString(encodeValue(p.fields[k]))
	}
	b.WriteByte('}')
	return b.String()
}

func encodeValue(v Value) string {
	switch v.Kind {
	case KindNum:
		return EncodeNum(v.Num)
	case KindStr:
		return "'" + v.Raw + "'"
	case KindList:
		var parts = make([]string, 0, len(v.List))
		var e Value
		for _, e = range v.List {
			parts = append(parts, encodeValue(e))
		}
		return "[" + strings.Join(parts, ",") + "]"
	case KindObj:
		if v.Obj == nil {
			return "{}"
		}
		return v.Obj.Encode()
	}
	return "x"
}

// EncodeTable rend une table (/vlan.b, /host.b, /acl.b) telle que the device
// l'attend en write : `[{…},{…}]`.
//
// Une table s'written ENTIRE. Cette plateforme n'a ni ajout ni suppression de ligne :
// l'interface reconstruit la liste complete depuis son tableau et la poste (`Qa`
// dans son JavaScript). Poster une table truncated d'une ligne SUPPRIME ce VLAN — ce
// qui est le comportement voulu quand on l'a decide, et une coupure de service
// quand on l'a oublie.
func EncodeTable(rows []*Page) string {
	var parts = make([]string, 0, len(rows))
	var r *Page
	for _, r = range rows {
		parts = append(parts, r.Encode())
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// EncodeNum rend un entier comme l'interface de the device l'written.
func EncodeNum(v uint32) string {
	var s = strconv.FormatUint(uint64(v), 16)
	if len(s)%2 == 1 {
		s = "0" + s
	}
	return "0x" + s
}

// --- L'analyseur ---

// Parse lit une page `*.b`. Le result est soit un objet, soit une liste
// d'objets (les tables : /vlan.b, /host.b, /acl.b, /!dhost.b).
//
// Une page qui n'est pas du SwOS ne rend PAS une page vide : elle rend une erreur.
// C'est ce qui distingue « ce switch ne configure pas de VLAN » de « the device
// m'a servi sa page d'accueil au lieu de ses data » — et il le fait, tous les
// chemins inconnus rendant l'interface complete en HTTP 200.
func Parse(body string) (*Page, []*Page, error) {
	var s = &scanner{src: body}
	s.ws()
	if s.eof() {
		return nil, nil, fmt.Errorf("page vide : the device n'a rien servi")
	}
	switch s.peek() {
	case '{':
		var page, err = s.object()
		if err != nil {
			return nil, nil, err
		}
		s.ws()
		if !s.eof() {
			return nil, nil, fmt.Errorf("data residuelles after l'objet, a l'offset %d : %s",
				s.i, excerpt(s.src[s.i:], 60))
		}
		return page, nil, nil
	case '[':
		var rows, err = s.rows()
		if err != nil {
			return nil, nil, err
		}
		s.ws()
		if !s.eof() {
			return nil, nil, fmt.Errorf("data residuelles after la table, a l'offset %d : %s",
				s.i, excerpt(s.src[s.i:], 60))
		}
		return nil, rows, nil
	}
	return nil, nil, fmt.Errorf("ce n'est pas une page SwOS : elle commence par %q, "+
		"expected « { » ou « [ » (%s)", s.peek(), excerpt(body, 80))
}

// ParseObject lit une page dont on ATTEND un objet. Rend une erreur si
// the device a servi une table — se tromper de forme ici, c'est lire une
// configuration there where il n'y a qu'une liste vide.
func ParseObject(body string) (*Page, error) {
	var page, rows, err = Parse(body)
	if err != nil {
		return nil, err
	}
	if page == nil {
		return nil, fmt.Errorf("objet expected, table de %d ligne(s) recue", len(rows))
	}
	return page, nil
}

// ParseTable lit une page dont on attend une table. Une table VIDE est un
// result legitime, et se distingue d'une erreur : sur cet device, `[]` veut
// dire « aucune rule ACL », pas « je n'ai pas su lire ».
func ParseTable(body string) ([]*Page, error) {
	var page, rows, err = Parse(body)
	if err != nil {
		return nil, err
	}
	if page != nil {
		return nil, fmt.Errorf("table attendue, objet a %d champ(s) recu", len(page.order))
	}
	if rows == nil {
		rows = []*Page{}
	}
	return rows, nil
}

// ParseBackup lit /backup.swb — la configuration ENTIRE en une seule request.
//
// La forme est celle d'un objet SANS accolades, dont les keys sont des NOMS DE
// PAGE : `vlan.b:[…],snmp.b:{…},.pwd.b:{pwd:”}`. C'est la source la plus precieuse
// de cette plateforme, a double titre : elle donne state complet pour le prix
// d'une lecture, et elle ENUMERATES, page par page, les champs actually
// INSCRIPTIBLES — ce qu'aucune documentation ne dit (cf. pages.go).
func ParseBackup(body string) (*Page, error) {
	var s = &scanner{src: body}
	var page, err = s.fields('\x00')
	if err != nil {
		return nil, err
	}
	s.ws()
	if !s.eof() {
		return nil, fmt.Errorf("data residuelles dans la sauvegarde, a l'offset %d : %s",
			s.i, excerpt(s.src[s.i:], 60))
	}
	return page, nil
}

type scanner struct {
	src string
	i   int
}

func (s *scanner) eof() bool  { return s.i >= len(s.src) }
func (s *scanner) peek() byte { return s.src[s.i] }

func (s *scanner) ws() {
	for s.i < len(s.src) {
		switch s.src[s.i] {
		case ' ', '\t', '\r', '\n':
			s.i++
		default:
			return
		}
	}
}

// object lit `{ key:valeur, … }`.
func (s *scanner) object() (*Page, error) {
	s.i++ // '{'
	var page, err = s.fields('}')
	if err != nil {
		return nil, err
	}
	s.ws()
	if s.eof() || s.peek() != '}' {
		return nil, fmt.Errorf("accolade fermante manquante a l'offset %d", s.i)
	}
	s.i++
	return page, nil
}

// fields lit une suite `key:valeur` separee par des virgules, jusqu'a `end`.
// `end` valant NUL, la suite va jusqu'a la fin de ingress : c'est la forme de
// /backup.swb.
func (s *scanner) fields(end byte) (*Page, error) {
	var page = NewPage()
	for {
		s.ws()
		if s.eof() {
			if end == '\x00' {
				return page, nil
			}
			return nil, fmt.Errorf("fin ingress inattendue a l'offset %d", s.i)
		}
		if s.peek() == end {
			return page, nil
		}
		var key, err = s.key()
		if err != nil {
			return nil, err
		}
		s.ws()
		if s.eof() || s.peek() != ':' {
			return nil, fmt.Errorf("« : » expected after la key %q, a l'offset %d", key, s.i)
		}
		s.i++
		var v Value
		v, err = s.value()
		if err != nil {
			return nil, fmt.Errorf("champ %q : %w", key, err)
		}
		// Une key repetee est une page qu'on ne sait pas lire : la last
		// gagnerait en silence, et writing qui suivrait porterait sur une valeur
		// que personne n'a vue.
		if page.Has(key) {
			return nil, fmt.Errorf("key %q servie deux fois a l'offset %d", key, s.i)
		}
		page.set(key, v)
		s.ws()
		if s.eof() {
			if end == '\x00' {
				return page, nil
			}
			return nil, fmt.Errorf("fin ingress inattendue after %q", key)
		}
		if s.peek() == ',' {
			s.i++
			continue
		}
		if s.peek() == end {
			return page, nil
		}
		return nil, fmt.Errorf("« , » ou fin d'objet expected after %q, obtenu %q a l'offset %d",
			key, s.peek(), s.i)
	}
}

// key lit un nom de champ. Le point et le point d'exclamation sont admis parce
// que /backup.swb se sert des NOMS DE PAGE comme keys (`vlan.b`, `.pwd.b`) ;
// aucune page de data n'en porte, les accepter ne relache donc rien d'utile.
func (s *scanner) key() (string, error) {
	var start = s.i
	for s.i < len(s.src) {
		var c = s.src[s.i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' || c == '.' || c == '!' {
			s.i++
			continue
		}
		break
	}
	if s.i == start {
		return "", fmt.Errorf("nom de champ expected a l'offset %d, obtenu %q", s.i, s.src[s.i])
	}
	return s.src[start:s.i], nil
}

func (s *scanner) value() (Value, error) {
	s.ws()
	if s.eof() {
		return Value{}, fmt.Errorf("valeur attendue, fin ingress")
	}
	switch s.peek() {
	case '{':
		var obj, err = s.object()
		if err != nil {
			return Value{}, err
		}
		return Value{Kind: KindObj, Obj: obj}, nil
	case '[':
		return s.array()
	case '\'':
		return s.str()
	}
	return s.number()
}

// array lit un tableau, dont les elements peuvent be des nombres, des strings
// ou des objets.
func (s *scanner) array() (Value, error) {
	s.i++ // '['
	var out = Value{Kind: KindList}
	for {
		s.ws()
		if s.eof() {
			return Value{}, fmt.Errorf("crochet fermant manquant")
		}
		if s.peek() == ']' {
			s.i++
			return out, nil
		}
		var v, err = s.value()
		if err != nil {
			return Value{}, err
		}
		out.List = append(out.List, v)
		s.ws()
		if s.eof() {
			return Value{}, fmt.Errorf("crochet fermant manquant")
		}
		if s.peek() == ',' {
			s.i++
			continue
		}
		if s.peek() == ']' {
			s.i++
			return out, nil
		}
		return Value{}, fmt.Errorf("« , » ou « ] » expected a l'offset %d, obtenu %q",
			s.i, s.peek())
	}
}

// rows lit une table `[{…},{…}]`. Les tables de SwOS ne portent que des objets ;
// y trouver autre chose est une page qu'on ne sait pas lire.
func (s *scanner) rows() ([]*Page, error) {
	var v, err = s.array()
	if err != nil {
		return nil, err
	}
	var out = []*Page{}
	var e Value
	for _, e = range v.List {
		if e.Kind != KindObj || e.Obj == nil {
			return nil, fmt.Errorf("table SwOS : ligne qui n'est pas un objet")
		}
		out = append(out, e.Obj)
	}
	return out, nil
}

// str lit `'…'`. Le contenu n'est PAS decode ici : une string mal encoded ne doit
// pas faire perdre la page entire, elle doit se signaler quand on lit son texte.
func (s *scanner) str() (Value, error) {
	s.i++ // '\''
	var start = s.i
	for s.i < len(s.src) && s.src[s.i] != '\'' {
		s.i++
	}
	if s.eof() {
		return Value{}, fmt.Errorf("apostrophe fermante manquante a partir de l'offset %d", start)
	}
	var raw = s.src[start:s.i]
	s.i++
	return Value{Kind: KindStr, Raw: raw}, nil
}

// number lit `0x…`. Le decimal nu est admis : la sauvegarde de configuration et
// les pages ne servent que de l'hexadecimal prefixe, mais un firmware qui
// omettrait le prefix donnerait une page illisible pour une raison purement
// cosmetique.
func (s *scanner) number() (Value, error) {
	var start = s.i
	var base = 10
	if strings.HasPrefix(s.src[s.i:], "0x") || strings.HasPrefix(s.src[s.i:], "0X") {
		s.i += 2
		base = 16
		start = s.i
	}
	for s.i < len(s.src) && isHexDigit(s.src[s.i]) {
		s.i++
	}
	if s.i == start {
		return Value{}, fmt.Errorf("nombre expected a l'offset %d", start)
	}
	var n, err = strconv.ParseUint(s.src[start:s.i], base, 64)
	if err != nil {
		return Value{}, fmt.Errorf("nombre illisible %q : %w", s.src[start:s.i], err)
	}
	// SwOS n'a pas de valeur au-dela de 32 bits : au-dela, c'est une page qu'on
	// lit mal, et la tronquer donnerait un masque de ports faux.
	if n > 0xFFFFFFFF {
		return Value{}, fmt.Errorf("nombre hors des 32 bits de SwOS : %q", s.src[start:s.i])
	}
	return Value{Kind: KindNum, Num: uint32(n)}, nil
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// decodeHexText decodes une string hex-encoded comme the device le fait.
//
// Un nombre IMPAIR de chiffres voit le dernier abandonne, comme dans le
// JavaScript de the device (`for(d=0; d+1<a.length; d+=2)`) : on lit ce que
// the device lit, y compris quand ce qu'il sert est bancal.
func decodeHexText(raw string) (string, bool) {
	if len(raw)%2 == 1 {
		raw = raw[:len(raw)-1]
	}
	var b, err = hex.DecodeString(raw)
	if err != nil {
		return "", false
	}
	// L'octet NUL termine : ces champs sont des tampons de taille fixe.
	var i int
	for i = 0; i < len(b); i++ {
		if b[i] == 0 {
			return string(b[:i]), true
		}
	}
	return string(b), true
}

// excerpt reduit une response a ce qui tient dans un message d'erreur. Sans
// extrait, « ce n'est pas une page SwOS » n'aide personne : c'est justement le
// message qu'on obtient quand the device sert son interface web complete a la
// place des data.
func excerpt(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
