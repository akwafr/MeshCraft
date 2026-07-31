package swos

// Ce que ce connecteur sait WRITE, et surtout ce qu'il ne sait pas.
//
// Declare ICI, dans le paquet qui written, et non dans la couche API : le jour where une
// intention de plus devient posable, la declaration et le code qui la pose changent
// dans le same repertoire. Une capability declared loin de writing finit toujours
// par annoncer autre chose que ce qui se passe actually — et une capability qui
// mente est pire que pas de capability du tout, parce qu'elle donne a l'exploitant une
// configuration qu'il croit applied.
//
// Meme mecanisme et same raison que instanton.WritableExtras : meshcraft POSE tout
// ce qui est supporte, considere the device a jour pour autant, et DIT ce qui est
// reste a quai.

// WritableExtras : les intentions AUTRES que l'appartenance VLAN que ce connecteur
// pose actually (cf. recon.ExtraIntents pour ce que le model sait porter :
// `poe`, `stp`, `dhcp_snooping`, `port_security`).
//
// VIDE, et ce n'est pas un oubli.
//
// C'est same un vide instructif, parce que la raison n'est PAS l'absence de ces
// fonctions sur la plateforme : SwOS sait faire trois des quatre. Le spanning-tree
// est there (/rstp.b `ena` par port), le snooping DHCP aussi (/sys.b `dtrp` et `ainf`),
// et la security de port also (/fwd.b `lck` et `lckf`). Le PoE est le seul
// absent, et seulement sur les modeles sans l'electronique correspondante.
//
// Ce qui manque est de NOTRE cote, et c'est une distinction qui compte : ces
// ecritures ne sont pas impossibles, elles ne sont pas faites. Et il y a une raison
// de ne pas les avoir codees a l'aveugle — sur SwOS, chacune de ces trois intentions
// vit dans une page qui porte AUSSI des champs capables de couper access. Ecrire le
// snooping DHCP, c'est rewrite /sys.b, donc renvoyer a the device `avln`, `allp`,
// `alla` et `allm` dans le same geste. Coder cela sans avoir pu eprouver une seule
// write sur du hardware serait exactement le genre de raccourci que ce projet
// refuse.
//
// Ce vide se lit a the UI, au lieu de se decouvrir sur le terrain.
var WritableExtras = map[string]bool{}

// Le pendant en LECTURE de ce vide n'est pas ici mais dans
// Capabilities().Monitoring.Unavailable (swos.go) : ce que la plateforme n'expose pas
// en supervision, avec son motif. Il y vit parce qu'il doit be lisible depuis le
// contrat driver.Capabilities pour un model donne, there where WritableExtras n'est
// consulte que par la pose.
//
// Et il y a un troisieme pendant, propre a cette plateforme, qui ne rentre dans aucun
// des deux champs du contrat : la politique de RETRAIT d'appartenance. Elle a changed —
// la pose retire now les appartenances que le plan ne demande plus, avec quatre
// refus qui protegent le VLAN de management, le VLAN natif effectif, les ports en
// « all » et les ports actifs d'un device dont on ne connait pas le VLAN
// d'administration. La raison de chacun est written au long dans apply.go
// (removalPolicy, planLeaves), a cote du code qui decide.
//
// Il y a un QUATRIEME pendant, et c'est le seul refus write qui reste sur le
// model VLAN de cette plateforme : ni « VLAN Receive » (/fwd.b `vlni`) ni « Force
// VLAN ID » (/fwd.b `fvid`) ne sont written, alors que les deux sont inscriptibles et
// already renvoyes a the device a chaque pose avec leur valeur lue. Le motif est dans
// ports.go (receivePolicy) : les resserrer JETTE du trafic, et le model standard
// n'exprime pas ce qu'il faudrait pour decider lequel. Ils sont en revanche LUS, et
// flagged from qu'ils contredisent le role du port (vlanAnomalies).

// WritesExtra dit si ce connecteur pose l'intention named.
func WritesExtra(name string) bool { return WritableExtras[name] }

// RemovalPolicy rend, en une phrase, ce que la pose retire et ce qu'elle refuse de
// retirer. Destine a the UI : l'exploitant doit pouvoir lire la limite sans ouvrir le
// code.
func RemovalPolicy() string { return removalPolicy() }

// ReceivePolicy rend, en une phrase, les deux champs de VLAN par port que ce
// connecteur does not write JAMAIS et pourquoi.
//
// Exportee pour la same raison que RemovalPolicy, et c'est la troisieme forme du same
// principe : ce qu'on ne fait pas doit be lisible depuis l'exterieur du paquet.
// `WritableExtras` dit les intentions non applied, `Monitoring.Unavailable` dit les
// metriques absentes, et ces deux phrases-ci disent ce que la pose s'interdit DANS une
// intention qu'elle honore par ailleurs — la nuance qu'aucun champ du contrat ne porte.
func ReceivePolicy() string { return receivePolicy() }
