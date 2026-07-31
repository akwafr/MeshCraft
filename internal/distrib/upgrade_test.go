package distrib

import "testing"

// La comparaison de versions de proxy, et le default qu'elle a porte.
//
// Ce paquet n'avait aucun test. Celui-ci existe pour une raison precise : la seule
// ligne de decision de tout le cycle de mise a jour — `candidate > current` — se
// trompait sur le cas le plus courant d'un parc de developpement, et se trompait dans
// le sens qui BLOQUE. Un proxy annonce `dev` was plus « recent » que tout horodagage
// de build, donc a jour pour toujours.
//
// Ce n'was pas visible : l'interface affichait « up_to_date », ce qui est exactement
// ce qu'on veut lire. Le proxy de reference est reste ainsi jusqu'a ce qu'on cherche
// pourquoi il annoncait `dev`.

// TestIsNewerNeLaisseAucunProxyBloque
//
// LE CAS QUI COMPTE est la troisieme ligne : `dev` → un horodatage. Sans elle, le
// controleur n'a plus aucun moyen de recuperer un proxy mal stampe, et la rule
// d'architecture « aucune intervention sur la machine du proxy, jamais » est morte.
func TestIsNewerNeLaisseAucunProxyBloque(t *testing.T) {
	var cas = []struct {
		candidate, current string
		attend             bool
		pourquoi           string
	}{
		{"20260729.213000", "20260726.181502", true,
			"deux horodatages : la comparaison lexicographique EST la chronologie"},
		{"20260726.181502", "20260729.213000", false,
			"on ne descend jamais en version sans epinglage explicite"},
		{"20260729.213000", "20260729.213000", false,
			"la same version n'est pas une mise a jour"},

		{"20260729.213000", "dev", true,
			"LE DEFAULT CORRIGE : « dev » se trie AFTER les chiffres, donc l'ancien " +
				"`candidate > current` rendait faux — un proxy stampe « dev » n'was plus " +
				"joignable par aucune mise a jour, et ne se reparait que sur sa machine"},
		{"20260729.213000", "hors-build", true,
			"le default de compilation actuel : same traitement, et c'est le but"},
		{"20260729.213000", "0.1.0", true,
			"une version produit applied there par erreur ne se situe pas dans le temps"},
		{"20260729.213000", "", true,
			"une courante vide n'est pas comparable non plus — la porte qui la refuse " +
				"est ailleurs (upgradeOffer attend que le proxy se present)"},

		{"dev", "20260726.181502", false,
			"on ne PROPOSE jamais l'inconnu : c'est la rule already written pour une " +
				"version cible vide"},
		{"", "20260726.181502", false, "cible vide : rien a proposer"},
		{"latest", "dev", false,
			"deux valeurs non comparables ne font pas une mise a jour, sinon un " +
				"controleur mal empaquete pousserait n'importe quoi en boucle"},

		{"2026072.213000", "dev", false, "sept chiffres de date : pas un horodatage"},
		{"20260729.21300", "dev", false, "cinq chiffres d'heure : pas un horodatage"},
		{"20260729213000", "dev", false, "le point est le separateur, il est exige"},
		{"20260729.213000 ", "dev", false,
			"un espace en queue vient d'un fichier mal lu, pas d'un build"},
		{"v20260729.213000", "dev", false, "un prefix n'est pas dans le format"},
	}
	var c = cas[0]
	for _, c = range cas {
		var got = IsNewer(c.candidate, c.current)
		if got != c.attend {
			t.Errorf("IsNewer(%q, %q) = %v, expected %v — %s",
				c.candidate, c.current, got, c.attend, c.pourquoi)
		}
	}
}

// TestIsNewerEstAntisymetrique : deux horodatages distincts ne peuvent pas be chacun
// plus recent que l'autre.
//
// Un control de coherence, et il attrape la faute qu'on ferait en « reparant » cette
// fonction a la va-vite — rendre vrai from que les deux different ferait osciller un parc
// entre deux versions, chaque heartbeat proposant l'autre.
func TestIsNewerEstAntisymetrique(t *testing.T) {
	var a, b = "20260726.181502", "20260729.213000"
	if IsNewer(a, b) && IsNewer(b, a) {
		t.Error("les deux sens sont vrais : un parc oscillerait entre deux versions")
	}
	if !IsNewer(b, a) {
		t.Error("le build le plus recent doit be propose")
	}
}
