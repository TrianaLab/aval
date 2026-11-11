module example.com/evidence-consumer

go 1.27.4

require github.com/TrianaLab/aval v0.0.0

// CI builds this example against the repository's own pkg/evidence. A real
// consumer drops the replace and requires a released version.
replace github.com/TrianaLab/aval => ../..
