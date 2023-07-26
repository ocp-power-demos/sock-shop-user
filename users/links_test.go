package users

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

func TestLinksSerizalization(t *testing.T) {
	nl := make(Links)
	link := fmt.Sprintf("http://%v/%v/%v", "user", "cards", "1234")
	nl["card"] = Href{link}
	nl["self"] = Href{link}
	fmt.Printf("Card with links is x [ %+v ]\n", nl)
	fmt.Printf("Here [ %+v ]\n", json.NewEncoder(os.Stdout).Encode(nl))
	//t.Errorf("Card with links is x [ %+v ]\n", nl)
}
