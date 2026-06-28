
package tui
import "testing"
func TestColonFormat(t *testing.T) {
    var d Decoder
    keys := d.Push([]byte{27,'[','5','7','3','5','2',';','1',':','1','u'})
    if len(keys) != 1 || keys[0].Kind != KeyArrowUp {
        t.Fatalf("got %v pending=%q", keys, d.pending)
    }
}
