package pinning

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const dg = "@sha256:c5d34a98396a4775e223c74e929162c35c5a2182901ba26b50ade939941ce14b"
const sha = "0123456789abcdef0123456789abcdef01234567"

// detect runs every detector that applies to path, with scope as the other
// in-scope files, and returns the refs found.
func detect(path, data string, scope ...string) []string {
	has := func(p string) bool { return p == path || slices.Contains(scope, p) }
	var refs []string
	for _, d := range Detectors {
		if d.Applies(path) {
			for _, f := range d.Detect(Input{Path: path, Data: []byte(data), Has: has}) {
				refs = append(refs, f.Ref)
			}
		}
	}
	return refs
}

// tc writes the library's name into a case, as <tc> or <TC>.
func tc(s string) string {
	return strings.NewReplacer("<tc>", "test"+"containers", "<TC>", "Test"+"containers").Replace(s)
}

func cmdRefs(command string, scope ...string) []string {
	var refs []string
	for _, f := range scanShell(CommandFile, command, func(p string) bool { return slices.Contains(scope, p) }) {
		refs = append(refs, f.Ref)
	}
	return refs
}

type fileCase struct {
	name, path, data string
	scope            []string
	want             []string // refs; nil means pinned
}

// The false-hit table per ecosystem: each mutable reference must be found
// (a miss means a repushed tag or a new release could be reused), and each
// pinned variant must not (a finding there is a needless miss).
var fileCases = []fileCase{
	// Container images.
	{name: "compose tag", path: "compose.yaml", data: "services:\n  db:\n    image: postgres:16\n", want: []string{"postgres:16"}},
	{name: "compose no tag", path: "x/docker-compose.yml", data: "services: {db: {image: x}}\n  - image: 'redis' # cache\n", want: []string{"x", "redis"}},
	{name: "compose digest", path: "compose.yaml", data: "    image: \"postgres:16" + dg + "\"\n", want: nil},
	{name: "compose variable", path: "compose.yaml", data: "    image: ${DB_IMAGE:-postgres:16}\n", want: []string{"${DB_IMAGE:-postgres:16}"}},
	{name: "helm template", path: "k8s/deploy.yaml", data: "      - image: {{ .Values.image }}\n", want: []string{"{{ .Values.image }}"}},
	{name: "k8s list item", path: "k8s/deploy.yaml", data: "      - name: app\n        image: ghcr.io/acme/app:1.2\n", want: []string{"ghcr.io/acme/app:1.2"}},
	{name: "helm template comment backslash", path: "templates/pod.yaml", data: "{{- if .Values.on }}\nspec:\n  containers:\n  - name: x # note \\\n    image: evil:latest\n{{- end }}\n", want: []string{"evil:latest"}},
	{name: "helm template tagged and explicit keys", path: "templates/pod.yaml", data: "{{- if .Values.on }}\n- !!str image: evil:latest\n  &a image: evil2:latest\n  ? image\n  : evil3:latest\n{{- end }}\n", want: []string{"evil:latest", "evil2:latest", "evil3:latest"}},
	{name: "helm template escaped key value on the next line", path: "templates/pod.yaml", data: "{{- if .Values.on }}\n  - name: a\n    \"\\x69mage\": # c\n      evil:latest\n{{- end }}\n", want: []string{`"\x69mage"`}},
	{name: "helm template explicit escaped key", path: "templates/pod.yaml", data: "{{- if .Values.on }}\n  - name: b\n    ? \"\\x69mage\"\n    : evil2:latest\n{{- end }}\n", want: []string{`? "\x69mage"`}},
	{name: "helm template multi-line escaped key", path: "templates/pod.yaml", data: "{{- if .Values.on }}\n  - name: c\n    ? \"ima\\\n    ge\"\n    : evil:latest\n{{- end }}\n", want: []string{`? "ima\`}},
	{name: "helm template escaped value is not a key", path: "templates/pod.yaml", data: "{{- if .Values.on }}\n  x: {{ regexMatch \"^\\d+$\" .x }}\n  name: \"\\x69mage\"\n  image: a" + dg + "\n{{- end }}\n", want: nil},
	{name: "yaml tagged and explicit keys", path: "k8s/pod.yaml", data: "- !!str image: evil:latest\n  &a image: evil2:latest\n  ? image\n  : evil3:latest\n", want: []string{"evil:latest", "evil2:latest", "evil3:latest"}},
	{name: "helm values mapping", path: "values.yaml", data: "image:\n  repository: x\n  tag: y\nimage: &v\n  tag: z\n# image: redis\n", want: nil},
	{name: "compose anchor", path: "compose.yaml", data: "    image: &img redis:latest\n    image: &pin \"redis" + dg + "\"\n", want: []string{"redis:latest"}},
	{name: "compose alias and block scalar", path: "compose.yaml", data: "    image: *img\n    image: >-\n      postgres:latest\n    image: |\n    image: !!str redis\n", want: []string{"*img", ">-", "|", "!!str redis"}},
	{name: "compose flow mapping", path: "compose.yaml", data: "  c: {image: \"mysql:8\", restart: always}\n  d: {restart: no, image: mysql" + dg + "}\n", want: []string{"mysql:8"}},
	{name: "compose image on the next line", path: "compose.yaml", data: "services:\n  db:\n    image:\n      postgres:latest\n  web:\n    image: # c\n\n      nginx" + dg + "\n", want: []string{"postgres:latest"}},
	{name: "helm values image mapping", path: "values.yaml", data: "image:\n  repository: x\n  tag: \"1\"\n", want: nil},
	{name: "compose json", path: "compose.json", data: `{"services":{"db":{"image":"postgres:latest"}}}`, want: []string{"postgres:latest"}},
	{name: "other json is not compose", path: "x.json", data: `{"image":"postgres:latest"}`, want: nil},
	{name: "compose quoted key", path: "compose.yaml", data: "    \"image\": \"nginx:latest\"\n    'image': nginx" + dg + "\n", want: []string{"nginx:latest"}},
	// Valid YAML is read by the parser alone, so a key the regexes misplace
	// cannot hide a decoded value (#116).
	{name: "yaml anchor-only value on the next line", path: "compose.yaml", data: "services:\n  web:\n    image: &a\n      alpine:latest\n", want: []string{"alpine:latest", "alpine:latest"}},
	{name: "yaml empty flow value on the next line", path: "compose.yaml", data: "services: {web: {image:\n  alpine:latest}}\n", want: []string{"alpine:latest", "alpine:latest}}"}},
	{name: "yaml lone cr line break", path: "compose.yaml", data: "services:\n  x:\r    image: busybox:latest\r  y:\n    image: alpine" + dg + "\n", want: []string{"busybox:latest"}},
	{name: "yaml nel line break", path: "compose.yaml", data: "services:\n  x:\u0085    image: busybox:latest\u0085  y:\n    image: alpine" + dg + "\n", want: []string{"busybox:latest"}},
	{name: "yaml ls line break", path: "compose.yaml", data: "services:\n  x:\u2028    image: busybox:latest\u2028  y:\n    image: alpine" + dg + "\n", want: []string{"busybox:latest"}},
	{name: "yaml ls line break in a list", path: "k8s/pod.yaml", data: "l:\n- a: b\u2028  image: evil:latest\n- image: a" + dg + "\n", want: []string{"evil:latest"}},
	{name: "compose dockerfile_inline", path: "compose.yaml", data: "services:\n  w:\n    build:\n      context: .\n      dockerfile_inline: |\n        FROM alpine:latest\n", want: []string{"alpine:latest"}},
	{name: "compose additional_contexts", path: "compose.yaml", data: "services:\n  w:\n    build:\n      additional_contexts:\n        base: docker-image://alpine:latest\n        p: docker-image://alpine" + dg + "\n        src: ../src\n  v:\n    build:\n      additional_contexts:\n      - base=docker-image://redis:7\n", want: []string{"alpine:latest", "redis:7"}},
	// The line pass runs on valid YAML too: Helm renders a block scalar with
	// tpl (extraContainers), and a KRM function config holds its image.
	{name: "yaml image in a block scalar", path: "values.yaml", data: "extraContainers: |\n  - name: sidecar\n    image: busybox:latest\n", want: []string{"busybox:latest"}},
	{name: "yaml binary key", path: "compose.yaml", data: "services: {app: {!!binary aW1hZ2U=: alpine:3.22}}\n", want: []string{"alpine:3.22"}},
	{name: "yaml tagged key", path: "compose.yaml", data: "services:\n  app:\n    !custom image: alpine:3.22\n    !!str name: x\n    !!binary bad: x\n", want: []string{"!custom image", "alpine:3.22", "!!binary bad"}},
	{name: "image tag transformer file", path: "k8s/retag.yaml", data: "apiVersion: builtin\nkind: ImageTagTransformer\nmetadata: {name: t}\nimageTag: {name: alpine, newTag: \"3.22\"}\n", want: []string{"ImageTagTransformer"}},
	{name: "workflow template expression is not a templated key", path: ".github/workflows/ci.yml", data: "jobs:\n  a:\n    steps:\n    - run: |\n        echo \"ref ${{ github.ref }}: ok\"\n", want: nil},
	{name: "templated key outside templates in valid yaml", path: "k8s/labels.yaml", data: "labels:\n  a: \"{{ $k }}: x\"\n", want: nil},
	{name: "helm templated key", path: "templates/pod.yaml", data: "spec:\n  containers:\n  - name: x\n    ima{{ \"ge\" }}: alpine:latest\n", want: []string{`ima{{ "ge" }}`}},
	{name: "helm templated key in a template", path: "templates/pod.yaml", data: "{{- range $k, $v := .Values.c }}\n  {{ $k }}: {{ $v }}\n{{- end }}\n  name: {{ .x }}\n# {{ a }}: b\n", want: []string{"{{ $k }}"}},
	{name: "helm template values on the next line", path: "templates/pod.yaml", data: "{{- if .x }}\n  - name: a\n    image: # c\n      evil:1\n    image: &a\n      evil:2\n    image: &b evil:3\n    image: &c\n      tag: x\n    image: {a: b, image:\n      evil:4}\n{{- end }}\n", want: []string{"evil:1", "evil:2", "evil:3", "{a: b, image:", "evil:4}"}},
	{name: "helm template value after a lone cr", path: "templates/pod.yaml", data: "x:\r  y:\r    image: {{ .v }}\n", want: []string{"{{ .v }}"}},
	{name: "helm tpl helper", path: "templates/_helpers.tpl", data: "{{- define \"demo.container\" }}\nimage: busybox:latest\n{{- end }}\n", want: []string{"busybox:latest"}},
	// YAML keys by decoded value (#116).
	{name: "yaml escaped key", path: "compose.yaml", data: "services:\n  db:\n    \"ima\\x67e\": evil:latest\n", want: []string{"evil:latest"}},
	{name: "yaml escaped key pinned", path: "compose.yaml", data: "services:\n  db:\n    \"ima\\u0067e\": \"redis" + dg + "\"\n", want: nil},
	{name: "yaml escaped key in a flow mapping", path: "k8s/pod.yml", data: "c: [{image: a" + dg + "}, {\"ima\\x67e\": evil:1, restart: no}]\n", want: []string{"evil:1"}},
	{name: "yaml escaped key with alias value", path: "compose.yaml", data: "x: &i redis:latest\n\"\\x69mage\": *i\n", want: []string{"*i"}},
	{name: "yaml escaped key with block value", path: "compose.yaml", data: "\"\\x69mage\": |\n  redis\n\"\\x69mage\": !t redis\n\"\\x69mage\": >-\n  redis\n", want: []string{"|", "!t redis", ">"}},
	{name: "yaml escaped key with list value", path: "compose.yaml", data: "\"\\x69mage\": [a]\n", want: []string{"image"}},
	{name: "yaml escaped key with mapping value", path: "values.yaml", data: "\"\\x69mage\":\n  tag: x\n", want: nil},
	{name: "json compose escaped key", path: "compose.json", data: `{"services":{"db":{"\u0069mage":"postgres:latest"},"w":{"image":"nginx` + dg + `"}}}`, want: []string{"postgres:latest"}},
	{name: "yaml explicit, tagged, anchored and nested keys", path: "compose.yaml", data: "? image\n: evil:0\nl:\n- !!str image: evil:1\n- &k image: evil:2\n- - image: evil:3\n- [image: evil:4]\n- *k : evil:5\n", want: []string{"evil:0", "evil:1", "evil:2", "evil:3", "evil:4", "evil:5"}},
	{name: "yaml undecodable escaped key", path: "compose.yaml", data: "\"ima\\qe\": evil:latest\n", want: []string{`"ima\qe"`}},
	{name: "yaml escaped key in invalid yaml", path: "compose.yaml", data: "x: @bad\n\"ima\\x67e\": evil:latest\n\"a\\tb\": evil:latest\n", want: []string{"evil:latest"}},
	{name: "kustomize images", path: "k8s/kustomization.yaml", data: "images:\n- name: nginx\n  newTag: \"1.27\"\n- name: redis\n  newName: ghcr.io/acme/redis\n- name: pg\n  newName: ghcr.io/acme/pg\n  newTag: \"16\"\n  digest: sha256:" + dg[8:] + "\n- name: untouched\n- \"n\\x61me\": a\n  \"newT\\x61g\": b\n", want: []string{"nginx:1.27", "ghcr.io/acme/redis", "a:b"}},
	{name: "kustomize tagSuffix", path: "kustomization.yaml", data: "images:\n- name: db\n  tagSuffix: -evil\n- name: pg\n  newTag: \"16\"\n  tagSuffix: -x\n  digest: sha256:" + dg[8:] + "\n", want: []string{"db:<tag>-evil", "pg:16-x"}},
	{name: "kustomize images pinned", path: "Kustomization", scope: []string{"deploy.yaml"}, data: "resources: [deploy.yaml]\nimages:\n- name: pg\n  digest: sha256:" + dg[8:] + "\n", want: nil},
	{name: "kustomize bad digest", path: "base/kustomization.yml", data: "images:\n- name: pg\n  digest: latest\n", want: []string{"pg@latest"}},
	{name: "kustomize invalid yaml", path: "kustomization.yaml", data: "images: [\n", want: []string{"kustomization.yaml"}},
	{name: "kustomize without images", path: "kustomization.yaml", scope: []string{"a.yaml"}, data: "resources: [a.yaml]\n---\n", want: nil},
	{name: "kustomize images not a list", path: "kustomization.yaml", data: "images: x\n", want: []string{"kustomization.yaml"}},
	{name: "kustomize images case-insensitive", path: "kustomization.yaml", data: "IMAGES:\n- NAME: nginx\n  NewTag: latest\n", want: []string{"nginx:latest"}},
	{name: "kustomize images via merge key", path: "kustomization.yaml", data: "<<: {images: [{name: nginx, newName: evil, newTag: latest}]}\n", want: []string{"evil:latest"}},
	{name: "kustomize entry merge key", path: "kustomization.yaml", data: "images:\n- &a {name: x, digest: sha256:" + dg[8:] + "}\n- <<: {newTag: latest}\n  name: nginx\n", want: []string{"nginx:latest"}},
	{name: "kustomize non-string key", path: "kustomization.yaml", data: "images:\n- {name: nginx, newTag: latest}\n? [a]\n: b\n", want: []string{"kustomization.yaml"}},
	{name: "kustomize value json cannot encode", path: "kustomization.yaml", data: "images:\n- {name: nginx, newTag: latest}\nx: .inf\n", want: []string{"kustomization.yaml"}},
	{name: "kustomize images type mismatch", path: "kustomization.yaml", data: "images:\n- name: [nginx]\n", want: []string{"kustomization.yaml"}},
	{name: "kustomize imageTags", path: "kustomization.yaml", data: "imageTags:\n- name: alpine\n  newTag: latest\n", want: []string{"alpine:latest"}},
	{name: "kustomize patch sets an image", path: "kustomization.yaml", scope: []string{"replicas.yaml"}, data: "patches:\n- path: replicas.yaml\n- target: {kind: Deployment}\n  patch: |-\n    - op: replace\n      path: /spec/template/spec/containers/0/image\n      value: alpine:latest\n", want: []string{"patches[1]"}},
	{name: "kustomize replacement sets an image", path: "kustomization.yaml", data: "replacements:\n- source: {kind: ConfigMap, name: c, fieldPath: data.img}\n  targets:\n  - select: {kind: Deployment}\n    fieldPaths: [spec.template.spec.containers.0.image]\n", want: []string{"replacements[0]"}},
	{name: "kustomize patches that set no image", path: "kustomization.yaml", scope: []string{"image-patch.yaml"}, data: "patches:\n- path: image-patch.yaml\n- patch: |-\n    - op: add\n      path: /spec/template/spec/containers/0/imagePullPolicy\n      value: Always\n- patch: |-\n    kind: Deployment\n    spec: {template: {spec: {imagePullSecrets: [{name: r}]}}}\n- patch: \"[\"\nreplacements:\n- targets: [{fieldPaths: [spec.template.spec.containers.0.imagePullPolicy]}]\n", want: nil},
	{name: "kustomize patch moves an image and a second document sets one", path: "kustomization.yaml", data: "patchesJson6902:\n- patch: '[{op: move, from: /spec/x/image, path: /spec/y}]'\npatches:\n- patch: |-\n    kind: A\n    ---\n    kind: Deployment\n    spec: {template: {spec: {containers: [{name: a, image: x}]}}}\nreplacements:\n- targets: [{fieldPath: image}]\n", want: []string{"x", "patches[0]", "patchesJson6902[0]", "replacements[0]"}},
	{name: "kustomize transformers, generators and helm charts", path: "k8s/kustomization.yaml", scope: []string{"k8s/labels.yaml"}, data: "transformers:\n- labels.yaml\n- |-\n  apiVersion: builtin\n  kind: ImageTagTransformer\n  metadata: {name: t}\n  imageTag: {name: alpine, newTag: \"3.22\"}\n- ../xform\ngenerators:\n- |-\n  kind: X\n  images: [{name: a, newTag: b}]\n- |-\n  kind: ConfigMapGenerator\nhelmCharts:\n- {name: c, valuesFile: v.yaml}\n- {name: c, valuesInline: {image: {tag: latest}}}\n", want: []string{"{tag: latest", "transformers[1]", "transformers[2]", "generators[0]", "helmCharts[0]", "helmCharts[1]"}},
	{name: "kustomize legacy patches set an image", path: "kustomization.yaml", scope: []string{"image-patch.yaml"}, data: "patchesStrategicMerge:\n- |-\n  kind: Deployment\n  spec: {template: {spec: {containers: [{name: a, IMAGE: x}]}}}\npatchesJson6902:\n- target: {kind: Deployment, name: a}\n  path: image-patch.yaml\n", want: []string{"patchesStrategicMerge[0]"}},
	// #126: a range loop's key variable directly under labels: or
	// annotations: names a label, so it is skipped; anywhere else, or a key
	// that is more than the variable, is still reported.
	{name: "helm range loop keys under labels and annotations", path: "templates/pod.yaml", data: "metadata:\n  labels:\n    app: x\n    {{- range $key, $value := .Values.labels }}\n    {{ $key | quote }}: {{ $value | quote }}\n    {{- end }}\n  annotations:\n    {{- range $k, $v := .Values.a }}\n    {{- $k }}: {{ $v }}\n    {{- end }}\nspec:\n  containers:\n  - name: a\n    {{- range $k, $v := .Values.c }}\n    {{ $k }}: {{ $v }}\n    {{- end }}\n  labels:\n    {{ $x }}: y\n    ima{{ $k }}: z\n  {{ $k }}: w\n", want: []string{"{{ $k }}", "{{ $x }}", "ima{{ $k }}", "{{ $k }}"}},
	// #126: patch files out of the scope and Helm charts Aval cannot read.
	{name: "kustomize patch files outside the scope", path: "k/kustomization.yaml", scope: []string{"k/in.yaml"}, data: "patches:\n- path: in.yaml\n- path: ../out.yaml\n- {PATH: gone.yaml, target: {kind: Deployment}}\n- {path: in.yaml, Path: in.yaml}\npatchesStrategicMerge:\n- in.yaml\n- /abs.yaml\npatchesJson6902:\n- {target: {kind: Deployment}, path: missing.yaml}\n", want: []string{"patches[1]", "patches[2]", "patches[3]", "patchesStrategicMerge[1]", "patchesJson6902[0]"}},
	{name: "kustomize helm charts remote, out of scope or local", path: "k/kustomization.yaml", scope: []string{"k/charts/app/Chart.yaml"}, data: "helmCharts:\n- {name: app}\n- {name: app, repo: https://charts.example.com, version: 1.0.0}\n- {name: other}\n- {name: app, Repo: oci://r.example.com/c}\n- {}\n- x\n", want: []string{"helmCharts[1]", "helmCharts[2]", "helmCharts[3]", "helmCharts[4]", "helmCharts[5]"}},
	{name: "kustomize helm chart home", path: "k/kustomization.yaml", scope: []string{"k/vendor/db/Chart.yaml"}, data: "helmGlobals: {chartHome: vendor}\nhelmCharts:\n- {name: db, valuesFile: v.yaml}\n- {name: app}\n", want: []string{"helmCharts[1]"}},
	{name: "kustomize helm absolute chart home", path: "k/kustomization.yaml", scope: []string{"k/abs/app/Chart.yaml"}, data: "helmGlobals: {chartHome: /abs}\nhelmCharts:\n- {name: app}\n", want: []string{"helmCharts[0]"}},
	{name: "kustomize helm backslash chart home", path: "k/kustomization.yaml", scope: []string{"k/\\abs/app/Chart.yaml"}, data: "helmGlobals: {chartHome: '\\abs'}\nhelmCharts:\n- {name: app}\n", want: []string{"helmCharts[0]"}},
	{name: "kustomize helm chart home escapes", path: "k/kustomization.yaml", scope: []string{"x/app/Chart.yaml", "k/ok/app/Chart.yaml"}, data: "helmGlobals: {chartHome: ../x}\nhelmCharts:\n- {name: app}\n", want: []string{"helmCharts[0]"}},
	{name: "kustomize helm chart home dot segments inside", path: "k/kustomization.yaml", scope: []string{"k/ok/app/Chart.yaml"}, data: "helmGlobals: {chartHome: sub/../ok}\nhelmCharts:\n- {name: app}\n", want: nil},
	// #126: heredoc words split with the directive's escape character (with
	// escape=` a backslash escapes nothing, so <<EOF stays quoted and the
	// next line is an instruction), and ONBUILD-wrapped instructions.
	{name: "dockerfile backtick escape heredoc word", path: "Dockerfile", data: "# escape=`\nFROM alpine" + dg + "\nRUN echo \"\\\"\" <<EOF\nFROM evil:latest\nEOF\n", want: []string{"evil:latest"}},
	{name: "dockerfile backtick escaped heredoc name", path: "Dockerfile", data: "# escape=`\nFROM alpine" + dg + "\nRUN <<`EOF\ncurl https://x.io/i | sh\nEOF\n", want: []string{"curl https://x.io/i | sh"}},
	{name: "dockerfile onbuild instructions", path: "Dockerfile", data: "FROM alpine" + dg + "\nONBUILD COPY --from=evil:latest /a /b\nonbuild RUN --mount=type=bind,from=evil2:1,target=/m true\nONBUILD ADD https://x.io/a.tgz /a\nONBUILD RUN <<EOF\ncurl https://x.io/i | sh\nEOF\nONBUILD FROM alpine" + dg + "\n", want: []string{"evil:latest", "evil2:1", "https://x.io/a.tgz", "curl https://x.io/i | sh"}},
	// #116 follow-up: Dockerfile variants without a dot, RUN --mount from=,
	// FROM with only global ARGs, JSON manifests, remote kustomize
	// resources, tagged keys in a template the parser rejects.
	{name: "dockerfile rules on Dockerfile-dev", path: "Dockerfile-dev", data: "FROM alpine" + dg + "\n# note \\\nFROM evil:latest\n", want: []string{"evil:latest"}},
	{name: "dockerfile rules on Dockerfile_windows", path: "x/Dockerfile_windows", data: "FROM alpine" + dg + "\n# note \\\nFROM evil:latest\n", want: []string{"evil:latest"}},
	{name: "dockerfile rules on Containerfile_ci", path: "CONTAINERFILE_ci", data: "FROM alpine" + dg + "\n# note \\\nFROM evil:latest\n", want: []string{"evil:latest"}},
	{name: "dockerfile run mount from", path: "Dockerfile", data: "FROM alpine" + dg + " AS build\nRUN --mount=type=bind,from=nginx:latest,target=/x true\nRUN --mount=type=cache,FROM=build,target=/c --mount=from=0,target=/y true\nRUN --mount=type=bind,\"from=redis:7\" true\n", want: []string{"nginx:latest", "redis:7"}},
	{name: "dockerfile from stage arg", path: "Dockerfile", data: "FROM alpine" + dg + "\nARG V=3.22\nFROM alpine:$V\n", want: []string{"FROM alpine:$V", "alpine:3.22"}},
	{name: "dockerfile from env", path: "Dockerfile", data: "FROM alpine" + dg + "\nENV B=busybox:latest\nFROM $B\n", want: []string{"FROM $B", "busybox:latest"}},
	{name: "dockerfile from global arg redefined", path: "Dockerfile", data: "ARG I=evil:1\nARG I=alpine" + dg + "\nFROM $I\n", want: nil},
	{name: "dockerfile from global arg unset", path: "Dockerfile", data: "ARG I=alpine" + dg + "\nARG I\nFROM $I\n", want: []string{"FROM $I"}},
	{name: "helm json template", path: "chart/templates/cm.json", data: "{\"spec\": {\"containers\": [{\n  \"image\": \"evil:latest\",\n  \"name\": \"x\"}]}}\n", want: []string{"evil:latest"}},
	{name: "package json is not a manifest", path: "templates.json", data: `{"image":"postgres:latest"}`, want: nil},
	{name: "kustomize remote and outside resources", path: "k8s/kustomization.yaml", scope: []string{"k8s/deploy.yaml", "base/kustomization.yaml", "k8s/sub/kustomization.yml", "k8s/c/Kustomization"},
		data: "resources:\n- deploy.yaml\n- ../base\n- sub\n- https://github.com/a/b//c?ref=main\n- github.com/a/b/c\n- git::https://x/y\n- ../other\n- {a: b}\ncomponents: [c, ../comp]\nbases: [../old]\n",
		want: []string{"resources[3]", "resources[4]", "resources[5]", "resources[6]", "resources[7]", "components[1]", "bases[0]"}},
	{name: "helm template tagged keys", path: "templates/pod.yaml", data: "{{- if .x }}\n  !!binary aW1hZ2U=: evil:latest\n  c: {!t key: v}\n  !!str name: x\n  d: !!str v\n{{- end }}\n", want: []string{"!!binary aW1hZ2U=", "!t key"}},
	{name: "dockerfile tag", path: "Dockerfile", data: "FROM golang:1.25\n", want: []string{"golang:1.25"}},
	{name: "dockerfile platform and stages", path: "build/Containerfile", data: "# c\nFROM --platform=$BUILDPLATFORM golang:1.25" + dg + " AS build\nFROM build AS test\nFROM scratch\nCOPY --from=build /a /b\nCOPY --from=0 /a /b\n", want: nil},
	{name: "dockerfile stage name is not an earlier stage", path: "Dockerfile", data: "FROM build AS build\n", want: []string{"build"}},
	{name: "dockerfile arg default", path: "app.Dockerfile", data: "ARG BASE=alpine:3.20\nFROM ${BASE}\n", want: []string{"alpine:3.20"}},
	{name: "dockerfile arg pinned", path: "Dockerfile.ci", data: "ARG BASE=alpine:3.20" + dg + "\nFROM $BASE\n", want: nil},
	{name: "dockerfile unresolved arg", path: "Dockerfile", data: "ARG BASE\nFROM ${BASE}\n", want: []string{"$BASE", "FROM ${BASE}"}},
	{name: "dockerfile copy from image", path: "Dockerfile", data: "FROM scratch\nCOPY --from=nginx:latest /a /b\n", want: []string{"nginx:latest"}},
	{name: "dockerfile remote add", path: "Dockerfile", data: "FROM scratch\nADD https://x.io/a.tgz /a\nADD --checksum=sha256:ab https://x.io/b.tgz /b\nADD a b\n", want: []string{"https://x.io/a.tgz"}},
	{name: "dockerfile run", path: "Dockerfile", data: "FROM scratch\nRUN --mount=type=cache,target=/r apk add --no-cache curl \\\n  && pip install requests==2.0\n", want: []string{"curl"}},
	{name: "dockerfile run global options", path: "Dockerfile", data: "FROM alpine" + dg + "\nRUN apk --no-cache add curl\nRUN apt-get -y install jq\n", want: []string{"curl", "jq"}},
	{name: "dockerfile syntax directive", path: "Dockerfile", data: "# syntax=docker/dockerfile:1\nFROM alpine" + dg + "\n", want: []string{"docker/dockerfile:1"}},
	{name: "dockerfile syntax directive pinned", path: "Dockerfile", data: "\n# check=skip=all\n#Syntax = docker/dockerfile:1" + dg + "\nFROM alpine" + dg + "\n# syntax=late:1\n", want: nil},
	// BuildKit line rules (#116): a comment line stands alone, continuations
	// join with nothing, comments and blank lines inside one are dropped,
	// and # escape= is honoured only among the leading parser directives.
	// These rules only add findings: the original line pass still runs, so
	// what it reads (a heredoc body as instructions, a continuation joined
	// with a space) is still reported.
	{name: "dockerfile heredoc on an old frontend", path: "Dockerfile", data: "# syntax=docker/dockerfile:1.2" + dg + "\nFROM alpine" + dg + "\nRUN true <<#x\nFROM alpine:3.22\n#x\nRUN echo final\n", want: []string{"alpine:3.22"}},
	{name: "dockerfile rules only on files named as dockerfiles", path: "frontend/dockerfile/dockerfile_args_test.go", data: "ARG IMG=alpine" + dg + "\nFROM $IMG\nARG IMG=alpine" + dg + "\nCOPY <<EOF /x\nFROM alpine" + dg + "\nEOF\n", want: nil},
	{name: "dockerfile rules on a containerfile variant", path: "Containerfile.dev", data: "ARG IMG=a:1\nFROM $IMG\nARG IMG=a:2\n", want: []string{"a:1", "a:2"}},
	{name: "dockerfile comment ending in backslash", path: "Dockerfile", data: "FROM alpine" + dg + "\n# note \\\nFROM evil:latest\n", want: []string{"evil:latest"}},
	{name: "dockerfile comment ending in backslash crlf", path: "Dockerfile", data: "FROM alpine" + dg + "\r\n  # note \\\r\nFROM evil:latest\r\n", want: []string{"evil:latest"}},
	{name: "dockerfile continuation joins with nothing", path: "Dockerfile", data: "FROM db:1\\\n6\n", want: []string{"db:1", "db:16"}},
	{name: "dockerfile continuation drops comments and blank lines", path: "Dockerfile", data: "FROM \\\n# c \\\n  \n  db:1\\\n  # d\n6\n", want: []string{"#", "db:16"}},
	{name: "dockerfile digest split across a continuation", path: "Dockerfile", data: "FROM alpine@sha256:" + dg[8:40] + "\\\n# c\n" + dg[40:] + "\n", want: []string{"alpine@sha256:" + dg[8:40]}},
	{name: "dockerfile escaped backslash is not a continuation", path: "Dockerfile", data: "FROM alpine" + dg + "\nRUN echo \\\\\nFROM evil:latest\n", want: []string{"evil:latest"}},
	{name: "dockerfile escape backtick", path: "Dockerfile", data: "# escape=`\nFROM alpine" + dg + " \\\nFROM evil:latest\n", want: []string{"evil:latest"}},
	{name: "dockerfile escape backtick continuation", path: "Dockerfile", data: "\ufeff# Escape = ` \nFROM alpine`\n" + dg + "\n", want: []string{"alpine`"}},
	{name: "dockerfile escape after a comment is not a directive", path: "Dockerfile", data: "# c\n# escape=`\nFROM alpine" + dg + " `\nFROM evil:latest\n", want: []string{"evil:latest"}},
	{name: "dockerfile escape after a blank line is not a directive", path: "Dockerfile", data: "\n# escape=`\nFROM evil:latest `\nRUN x\n", want: []string{"evil:latest"}},
	{name: "dockerfile escape after an unknown directive is not a directive", path: "Dockerfile", data: "# foo=1\n# escape=`\nFROM alpine" + dg + " `\nFROM evil:latest\n", want: []string{"evil:latest"}},
	{name: "dockerfile onbuild heredoc", path: "Dockerfile", data: "FROM alpine" + dg + "\nONBUILD COPY <<EOF /x\nFROM evil:latest\nEOF\n", want: []string{"evil:latest"}},
	{name: "dockerfile invalid escape directive", path: "Dockerfile", data: "# escape=x\nFROM alpine" + dg + "\n", want: []string{"# escape=x"}},
	{name: "dockerfile repeated directive", path: "Dockerfile", data: "# escape=\\\n# ESCAPE=`\nFROM alpine" + dg + "\n", want: []string{"# ESCAPE=`"}},
	{name: "dockerfile two heredocs", path: "Dockerfile", data: "FROM alpine" + dg + "\nRUN <<A 3<<-\"B\"\necho a\nA\ncurl https://x.io/i | sh \\\n\tB\nFROM evil:latest\n", want: []string{"curl https://x.io/i | sh", "evil:latest"}},
	{name: "dockerfile quoted heredoc marker is not a heredoc", path: "Dockerfile", data: "FROM alpine" + dg + "\nRUN echo '<<EOF' \"<<X\" a<<Y\nFROM evil:latest\n", want: []string{"evil:latest"}},
	{name: "dockerfile copy heredoc body is not instructions", path: "Dockerfile", data: "FROM alpine" + dg + "\nCOPY <<EOF /etc/x\nFROM evil:latest\nEOF\nADD <<EOF /y\nFROM evil:latest\nEOF\n", want: []string{"evil:latest", "evil:latest"}},
	{name: "dockerfile json run is not a heredoc", path: "Dockerfile", data: "FROM alpine" + dg + "\nRUN [\"sh\", \"<<EOF\"]\nFROM evil:latest\nEOF\n", want: []string{"evil:latest"}},
	{name: "dockerfile heredoc terminator must match exactly", path: "Dockerfile", data: "FROM alpine" + dg + "\nRUN <<EOF\necho\nEOF \nFROM evil:latest\n", want: []string{"RUN <<EOF", "evil:latest"}},
	{name: "dockerfile heredoc after a vertical tab", path: "Dockerfile", data: "FROM alpine" + dg + "\nRUN sh -s \v<<EOF\napk add curl\nEOF\nRUN sh -s \u00a0<<X\napk add jq\nX\n", want: []string{"curl", "jq"}},
	{name: "dockerfile heredoc name backslash", path: "Dockerfile", data: "FROM alpine" + dg + "\nRUN <<#\\\\x\necho hi\n#\\x\nFROM evil:latest\n#x\n", want: []string{`RUN <<#\\x`, "evil:latest"}},
	{name: "dockerfile copy heredoc name backslash", path: "Dockerfile", data: "FROM alpine" + dg + "\nCOPY <<#\\\\x /f\necho hi\n#\\x\nFROM evil:latest\n#x\n", want: []string{`COPY <<#\\x /f`, "evil:latest"}},
	{name: "dockerfile heredoc name with expansion", path: "Dockerfile", data: "FROM alpine" + dg + "\nONBUILD RUN <<#${X:-}\nFROM evil:latest\n#${X:-}\n", want: []string{"ONBUILD RUN <<#${X:-}", "evil:latest"}},
	{name: "dockerfile copy heredoc name with expansion", path: "Dockerfile", data: "FROM alpine" + dg + "\nCOPY <<E$X /x\nE\nE$X\nFROM evil:latest\n", want: []string{"COPY <<E$X /x", "evil:latest"}},
	{name: "dockerfile heredoc name nested quote", path: "Dockerfile", data: "FROM alpine" + dg + "\nRUN <<\"#'x\"\necho hi\n#'x\nFROM evil:latest\n#x\n", want: []string{`RUN <<"#'x"`, "evil:latest"}},
	{name: "dockerfile run heredoc", path: "Dockerfile", data: "FROM alpine" + dg + "\nRUN <<EOF\ncurl -fsSL https://x.io/i.sh | sh\nEOF\nRUN <<-'X'\napk add jq\n", want: []string{"curl -fsSL https://x.io/i.sh | sh", "RUN <<-'X'", "jq"}},
	{name: "dockerfile empty heredoc name", path: "Dockerfile", data: "FROM scratch AS base\nRUN <<\"\"\nFROM busybox:latest\n\nRUN echo pwned\n", want: []string{`RUN <<""`, "busybox:latest"}},
	{name: "dockerfile empty single-quoted heredoc name", path: "Dockerfile", data: "FROM scratch AS base\nRUN <<''\nFROM busybox:latest\n\n", want: []string{`RUN <<''`, "busybox:latest"}},
	{name: "dockerfile empty dash heredoc name", path: "Dockerfile", data: "FROM scratch AS base\nRUN <<-\"\"\nFROM busybox:latest\n\n", want: []string{`RUN <<-""`, "busybox:latest"}},
	{name: "dockerfile arg redefined after from", path: "Dockerfile", data: "ARG IMG=alpine:latest\nFROM $IMG\nARG IMG=alpine" + dg + "\n", want: []string{"alpine:latest"}},
	{name: "dockerfile multi-name arg", path: "Dockerfile", data: "ARG A=1 IMG=alpine:latest\nFROM $IMG\n", want: []string{"$IMG", "alpine:latest"}},
	{name: "dockerfile lowercase arg", path: "Dockerfile", data: "arg IMG=alpine:latest\nFROM ${IMG}\n", want: []string{"$IMG", "alpine:latest"}},
	{name: "dockerfile arg redeclared in a stage", path: "Dockerfile", data: "ARG IMG=alpine" + dg + "\nFROM $IMG\nARG IMG\nRUN echo $IMG\n", want: nil},

	// Go.
	{name: "go.mod without go.sum", path: "go.mod", data: "module x\n\nrequire example.com/y v1.0.0\n", want: []string{"go.mod"}},
	{name: "go.mod with go.sum", path: "m/go.mod", data: "module x\nrequire (\n)\n", scope: []string{"m/go.sum"}, want: nil},
	{name: "go.sum of another module", path: "m/go.mod", data: "require y v1\n", scope: []string{"go.sum"}, want: []string{"m/go.mod"}},
	{name: "go.mod without requirements", path: "go.mod", data: "module x\n\ngo 1.25\n", want: nil},

	// npm.
	{name: "package.json ranges", path: "package.json", data: "{\n  \"dependencies\": {\n    \"a\": \"^1.0.0\",\n    \"b\": \"1.2.3\"\n  },\n  \"devDependencies\": {\"c\": \"latest\", \"d\": \"*\", \"e\": \"github:u/r\"}\n}\n", want: []string{"a@^1.0.0", "c@latest", "d@*", "e@github:u/r"}},
	{name: "package.json exact, no lockfile", path: "package.json", data: `{"dependencies": {"a": "1.2.3", "w": "workspace:*", "f": "file:../f", "g": "git+https://x/y#` + sha + `", "n": "npm:z@2.0.0"}}`, want: []string{"package.json"}},
	{name: "package.json with lockfile", path: "web/package.json", data: `{"dependencies": {"a": "^1"}}`, scope: []string{"web/package-lock.json"}, want: nil},
	{name: "package.json workspace lockfile", path: "pkgs/a/package.json", data: `{"dependencies": {"a": "^1"}}`, scope: []string{"pnpm-lock.yaml"}, want: nil},
	{name: "package.json without dependencies", path: "package.json", data: `{"name": "x", "dependencies": "bad"}`, want: nil},
	{name: "package.json invalid", path: "package.json", data: `{`, want: []string{"package.json"}},

	// Container images in test code. The word is spliced in by tc so this
	// file, in the scope of Aval's own hermetic contract, does not mention it.
	// detectors.go and pinning.go do mention it: a quoted name:tag added there
	// would make Aval's own run a miss.
	{name: "tc go", path: "db_test.go", data: tc("import \"github.com/<tc>/<tc>-go/modules/postgres\"\nc, _ := postgres.Run(ctx, \"postgres:16-alpine\")\nr, _ := redis.Run(ctx, `redis:7`)\n"), want: []string{"postgres:16-alpine", "redis:7"}},
	{name: "tc python", path: "tests/conftest.py", data: tc("from <tc>.postgres import PostgresContainer\nwith PostgresContainer('postgres:16') as pg:\n    pass\nPostgresContainer('postgres:16" + dg + "')\n"), want: []string{"postgres:16"}},
	{name: "tc java", path: "src/test/java/DbIT.java", data: tc("import org.<tc>.containers.PostgreSQLContainer;\nnew PostgreSQLContainer<>(DockerImageName.parse(\"ghcr.io/acme/pg:16.4\"));\nnew GenericContainer(\"redis:7" + dg + "\");\n"), want: []string{"ghcr.io/acme/pg:16.4"}},
	{name: "tc kotlin", path: "DbTest.kt", data: tc("import org.<tc>.containers.GenericContainer\nval c = GenericContainer(\"localhost:5000/acme/app:dev\")\nval p = GenericContainer(\"redis:7" + dg + "\")\n"), want: []string{"localhost:5000/acme/app:dev"}},
	{name: "tc typescript", path: "db.test.ts", data: tc("import { GenericContainer } from \"<tc>\";\nawait new GenericContainer(\"redis:7.4\").start();\nnew GenericContainer(\"redis:7" + dg + "\");\n"), want: []string{"redis:7.4"}},
	{name: "tc javascript", path: "db.test.js", data: tc("const { GenericContainer } = require('<tc>');\nnew GenericContainer('nginx:latest');\nnew GenericContainer('redis:7" + dg + "');\n"), want: []string{"nginx:latest"}},
	{name: "tc csharp", path: "DbTests.cs", data: tc("using <TC>.PostgreSql;\nnew PostgreSqlBuilder().WithImage(\"postgres:16\").Build();\nnew PostgreSqlBuilder().WithImage(\"postgres:16" + dg + "\").Build();\n"), want: []string{"postgres:16"}},
	{name: "tc pinned", path: "db_test.go", data: tc("// <tc>\npostgres.Run(ctx, \"postgres:16" + dg + "\")\nGenericContainer('postgres" + dg + "')\n"), want: nil},
	{name: "tc untagged name is not reported", path: "db_test.go", data: tc("// <tc>\npostgres.Run(ctx, \"postgres\")\n"), want: nil},
	{name: "tc host port and urls", path: "db_test.py", data: tc("# <tc>\nhost = 'localhost:5432'\nip = \"127.0.0.1:8080\"\ndb = 'db.internal:5432'\nurl = \"jdbc:postgresql://localhost:5432/test\"\nr = 'redis://cache:6379'\nn = 'db:5432'\nc = \"cache:6379\"\nt = '12:30'\nd = 'sha256:" + strings.Repeat("a", 64) + "'\n"), want: nil},
	{name: "tc log strings", path: "db_test.go", data: tc("// <tc>\nt.Logf(\"started postgres:16 at %s\", h)\nlog.Println(\"error: \", err)\nfmt.Sprintf(\"%s:%d\", h, p)\n"), want: nil},
	{name: "source without the library is not read", path: "db_test.go", data: tc("docker.Run(ctx, \"postgres:16\")\n"), want: nil},
	{name: "tc in another language is not read", path: "db_test.rb", data: tc("# <tc>\nx = 'postgres:16'\n"), want: nil},

	// Python.
	{name: "requirements unpinned", path: "requirements.txt", data: "# c\nflask>=2\nrequests\n-r base.txt\n--index-url https://x\ndjango==4.*\nidna==3.7 ; python_version > '3'\nurllib3 == 2.2.1  # ok\n-e .\n-e git+https://g/x@main#egg=x\nx @ git+https://g/x@" + sha + "\ny @ https://h/y.tar.gz#sha256=ab\nz @ https://h/z.tar.gz\n-e\n./local\n", want: []string{"flask>=2", "requests", "base.txt", "django==4.*", "git+https://g/x@main#egg=x", "z@https://h/z.tar.gz"}},
	{name: "requirements includes", path: "ci/requirements.txt", data: "-r base.txt\n-c ../constraints.txt\n--requirement=dev.txt\n-rother.txt\n--constraint x.txt\n--requirements-y\n--constraint=\n-r\n", scope: []string{"ci/base.txt", "constraints.txt"}, want: []string{"dev.txt", "other.txt", "x.txt", "--constraint="}},
	{name: "requirements with hashes", path: "dev-requirements.txt", data: "a==1.0 \\\n    --hash=sha256:ab\nb==2.0\n./local\n", want: []string{"b==2.0"}},
	{name: "requirements range with ==", path: "requirements-dev.txt", data: "a==1.0,<2\n", want: []string{"a==1.0,<2"}},
	{name: "requirements directory", path: "requirements/dev.txt", data: "requests>=2\n", want: []string{"requests>=2"}},
	{name: "poetry without lock", path: "pyproject.toml", data: "[tool.poetry]\nname='x'\n", want: []string{"pyproject.toml"}},
	{name: "poetry with lock", path: "pyproject.toml", data: "[tool.poetry]\n", scope: []string{"poetry.lock"}, want: nil},
	{name: "pyproject without dependencies", path: "pyproject.toml", data: "[project]\nname = 'x'\ndependencies = []\n", want: nil},
	{name: "pyproject dependencies without lock", path: "pyproject.toml", data: "[project]\ndependencies = [\n  \"requests>=2\",\n]\n", want: []string{"pyproject.toml"}},
	{name: "pyproject optional dependencies without lock", path: "pyproject.toml", data: "[project]\n[project.optional-dependencies]\ntest = ['pytest']\n", want: []string{"pyproject.toml"}},
	{name: "pyproject dependencies with uv.lock", path: "pkgs/a/pyproject.toml", data: "[project]\ndependencies = [\"requests>=2\"]\n", scope: []string{"uv.lock"}, want: nil},
	{name: "pipfile without lock", path: "Pipfile", data: "[packages]\n", want: []string{"Pipfile"}},
	{name: "pipfile with lock", path: "Pipfile", data: "[packages]\n", scope: []string{"Pipfile.lock"}, want: nil},

	// Rust.
	{name: "cargo without lock", path: "Cargo.toml", data: "[package]\n[dependencies]\nserde = \"1\"\n", want: []string{"Cargo.toml"}},
	{name: "cargo target deps", path: "Cargo.toml", data: "[target.'cfg(unix)'.dev-dependencies]\n", want: []string{"Cargo.toml"}},
	{name: "cargo workspace lock", path: "crates/a/Cargo.toml", data: "[dependencies]\n", scope: []string{"Cargo.lock"}, want: nil},
	{name: "lockfile search ends at the root", path: "/w/Cargo.toml", data: "[dependencies]\n", want: []string{"/w/Cargo.toml"}},
	{name: "cargo without dependencies", path: "Cargo.toml", data: "[package]\nname = \"x\"\n", want: nil},

	// Java.
	{name: "maven moving versions", path: "pom.xml", data: "<modelVersion>4.0.0</modelVersion>\n<version>1.0-SNAPSHOT</version>\n<junit.version>LATEST</junit.version> <version>[1.0,2.0)</version>\n<version>RELEASE</version>\n<version>${junit.version}</version>\n<version>5.10.2</version>\n",
		want: []string{"<version>1.0-SNAPSHOT</", "<junit.version>LATEST</", "<version>[1.0,2.0)</", "<version>RELEASE</"}},
	{name: "gradle moving versions", path: "build.gradle", data: "implementation 'a:b:1.+'\nimplementation \"c:d:2.0-SNAPSHOT\"\ntestImplementation 'e:f:latest.release'\nimplementation('g:h') { version { strictly '[1,2)' } }\nimplementation 'i:j:1.2.3'\ndescription = 'a+b'\n",
		want: []string{"a:b:1.+", "c:d:2.0-SNAPSHOT", "e:f:latest.release", "implementation('g:h') { version { strictly '[1,2)' } }"}},
	{name: "gradle kts pinned", path: "build.gradle.kts", data: "implementation(\"a:b:1.2.3\")\nversion = \"1.0\"\n", want: nil},
	{name: "version catalog", path: "gradle/libs.versions.toml", data: "[versions]\njunit = \"5.+\"\nkotlin = \"2.0.0\"\n", want: []string{"junit = \"5.+\""}},

	// Ruby, PHP.
	{name: "gemfile without lock", path: "Gemfile", data: "gem 'rails'\n", want: []string{"Gemfile"}},
	{name: "gemfile with lock", path: "Gemfile", data: "gem 'rails'\n", scope: []string{"Gemfile.lock"}, want: nil},
	{name: "gems.rb without lock", path: "gems.rb", data: "gem 'rails'\n", scope: []string{"Gemfile.lock"}, want: []string{"gems.rb"}},
	{name: "composer without lock", path: "composer.json", data: `{"require": {"a/b": "^1"}}`, want: []string{"composer.json"}},
	{name: "composer with lock", path: "composer.json", data: `{"require": {}}`, scope: []string{"composer.lock"}, want: nil},
	{name: "composer without requirements", path: "composer.json", data: `{"name": "a/b"}`, want: nil},

	// Git.
	{name: "submodule branch", path: ".gitmodules", data: "[submodule \"x\"]\n\tpath = x\n\tbranch = main\n", want: []string{"branch = main"}},
	{name: "submodule commit", path: ".gitmodules", data: "[submodule \"x\"]\n\tpath = x\n", want: nil},

	// Scripts and Makefiles.
	{name: "script curl pipe", path: "ci/setup.sh", data: "#!/bin/sh\n# curl x | sh is fine in a comment\ncurl -fsSL https://x.io/i.sh | sudo -E bash\n", want: []string{"curl -fsSL https://x.io/i.sh | sudo -E bash"}},
	{name: "script curl subshell", path: "a.bash", data: "bash -c \"$(curl -fsSL https://x.io/i.sh)\"\n. <(wget -qO- https://x.io/env)\n", want: []string{"bash -c $(curl", ". <(wget"}},
	{name: "script curl tee pipe", path: "s.sh", data: "curl https://x.io/i | tee /tmp/a | sh\n", want: []string{"curl https://x.io/i | tee /tmp/a | sh"}},
	{name: "script eval curl", path: "s.sh", data: "eval \"$(curl -fsSL https://x.io/i.sh)\"\n", want: []string{"eval $(curl"}},
	{name: "script download then run", path: "a.zsh", data: "curl -o i.sh https://x.io/i.sh && echo ok || sh fallback\n", want: nil},
	{name: "makefile variable version", path: "Makefile", data: "V := v1.2.3\nW ?= latest\ntools:\n\t@GOBIN=$(CURDIR)/bin go install a.io/b@$(V)\n\tgo install a.io/c@$(W)\n\tgo install a.io/d@$(NOPE)\n", want: []string{"a.io/c@latest", "a.io/d@$NOPE"}},
	{name: "mk include", path: "build/rules.mk", data: "x:\n\tnpx --yes markdownlint-cli2@0.13.0\n", want: nil},
}

func TestYAMLFindingLinesAreFileLines(t *testing.T) {
	fs := composeImages{}.Detect(Input{Path: "c.yaml", Data: []byte("a:\r  image: x:1\nb: 1\u2028image: y:1\n")})
	if len(fs) != 2 || fs[0].Line != 1 || fs[1].Line != 2 {
		t.Fatalf("%v", fs)
	}
}

func TestDetectorsFileCases(t *testing.T) {
	for _, tc := range fileCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detect(tc.path, tc.data, tc.scope...); !slices.Equal(got, tc.want) {
				t.Fatalf("refs = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestNoRegressionAgainstMain: testdata/main-findings.json holds what the
// scanner at cb81760 (before the parser-based detection, #116) found for
// each fileCases input, as "line ecosystem ref". The scanner now must find
// all of it: parser findings only add. A case added later has no entry and
// is skipped. No detector finding is in the scan ecosystem, which
// scope.allowMutable cannot acknowledge: that is only for an unreadable file.
func TestNoRegressionAgainstMain(t *testing.T) {
	data, err := os.ReadFile("testdata/main-findings.json")
	if err != nil {
		t.Fatal(err)
	}
	var frozen map[string][]string
	if err := json.Unmarshal(data, &frozen); err != nil {
		t.Fatal(err)
	}
	if len(frozen) < 100 {
		t.Fatalf("only %d frozen cases", len(frozen))
	}
	for _, tc := range fileCases {
		has := func(p string) bool { return p == tc.path || slices.Contains(tc.scope, p) }
		var got []string
		for _, d := range Detectors {
			if d.Applies(tc.path) {
				for _, f := range d.Detect(Input{Path: tc.path, Data: []byte(tc.data), Has: has}) {
					got = append(got, fmt.Sprintf("%d %s %s", f.Line, f.Ecosystem, f.Ref))
					if f.Ecosystem == "scan" {
						t.Errorf("%s: scan finding %+v", tc.name, f)
					}
				}
			}
		}
		for _, w := range frozen[tc.name] {
			if !slices.Contains(got, w) {
				t.Errorf("%s: main found %q, now %q", tc.name, w, got)
			}
		}
	}
}

var commandCases = []struct {
	name, cmd string
	scope     []string
	want      []string
}{
	{name: "plain go test", cmd: "go test -race ./...", want: nil},
	{name: "go install latest", cmd: "go install golang.org/x/tools/cmd/stringer@latest", want: []string{"golang.org/x/tools/cmd/stringer@latest"}},
	{name: "go run branch", cmd: "go run -tags x example.com/t@main", want: []string{"example.com/t@main"}},
	{name: "go pinned", cmd: "go install a.io/b@v1.2.3 && go run a.io/c@v0.0.0-20240101000000-" + sha[:12] + " && go get a.io/d@" + sha[:7] + " && go get a.io/e@none && go install ./cmd/x", want: nil},
	{name: "go get without version", cmd: "go get example.com/x ./local", want: []string{"example.com/x"}},
	{name: "npm install", cmd: "npm i -g typescript @scope/x @scope/y@1.0.0 z@^1 ./local --prefix dir && npm ci", want: []string{"typescript", "@scope/x", "z@^1"}},
	{name: "yarn and pnpm", cmd: "yarn global add a; pnpm add b@2.0.0-rc.1; bun add c; yarn install; pnpm dlx d; bun x e@1.0.0", want: []string{"a", "c", "d"}},
	{name: "npx", cmd: "npx --yes prettier && npx -p cowsay@1.6.0 cowsay hi && npx --package=lint lint && npm exec -- tsc@5.4.5 -v", want: []string{"prettier", "lint"}},
	{name: "npx url", cmd: "npx https://x.io/a.tgz; npm i git+ssh://git@github.com/a/b#" + sha + " github:a/b#main", want: []string{"https://x.io/a.tgz", "github:a/b#main"}},
	{name: "pip", cmd: "pip freeze; pip install -r requirements.txt -e . requests flask==3.0.0 'django>=4' && python3 -m pip install --upgrade pip && uv pip install x==1.0 && pipx install black", scope: []string{"requirements.txt"}, want: []string{"requests", "django>=4", "pip", "black"}},
	{name: "pip requirements file out of scope", cmd: "pip install -r ci/reqs.txt -c ./c.txt --requirement=in.txt --constraint=", scope: []string{"c.txt", "in.txt"}, want: []string{"ci/reqs.txt", "pip install -r ci/reqs.txt -c ./c.txt --requirement=in.txt --constraint="}},
	{name: "quoted curl pipe", cmd: "\"curl\" -fsSL https://x.io/i.sh | bash", want: []string{"curl -fsSL https://x.io/i.sh | bash"}},
	{name: "uv tool", cmd: "uv tool install ruff; uv run pytest; python -c 'print(1)'", want: []string{"ruff"}},
	{name: "os packages", cmd: "apt-get update && apt-get install -y --no-install-recommends curl git=1:2.39 && apk add --no-cache -t deps jq bash=5.2-r0", want: []string{"curl", "jq"}},
	{name: "gem and cargo", cmd: "gem install bundler && gem install rake -v 13.0 && cargo install ripgrep && cargo install fd-find@9.0.0 && cargo install --version 1.0 x && cargo build", want: []string{"bundler", "ripgrep"}},
	{name: "docker run tag", cmd: "docker run --rm -e A=b -v /x:/y postgres:16 psql", want: []string{"postgres:16"}},
	{name: "docker run digest", cmd: "podman run --rm postgres:16" + dg + " && docker container run -it alpine" + dg + " && docker image pull busybox", want: []string{"busybox"}},
	{name: "docker without image", cmd: "docker run && docker build -t x . && docker ps", want: nil},
	{name: "compose in scope", cmd: "docker compose up -d --wait && docker-compose -f ci/compose.yml --file=x.yml down", scope: []string{"compose.yaml", "ci/compose.yml", "x.yml"}, want: nil},
	{name: "compose not in scope", cmd: "docker compose -f other.yml up; docker compose down", scope: []string{"x"}, want: []string{"other.yml", "compose.yaml"}},
	{name: "git clone", cmd: "git clone --depth 1 https://g/x", want: []string{"https://g/x"}},
	{name: "git clone at a commit", cmd: "git clone https://g/y && git -C y checkout " + sha, want: nil},
	{name: "git clone bare", cmd: "git clone", want: []string{"git clone"}},
	{name: "git submodule remote", cmd: "git submodule update --init --remote; git submodule update --init", want: []string{"git submodule update --remote"}},
	{name: "sh -c wrapper", cmd: "sh -c REDIS=1 sudo env -i X=1 nohup go install a.io/b@master", want: []string{"a.io/b@master"}},
	{name: "conditional", cmd: "if true; then npm i left-pad; fi", want: []string{"left-pad"}},
	{name: "redirections", cmd: "pip install 'requests>=2' > log 2>err && npm i x@1.0.0 >>log </dev/null", want: []string{"requests>=2"}},
	{name: "only assignments", cmd: "A=1 B=2", want: nil},
	{name: "curl pipe python", cmd: "wget -qO- https://x.io/get.py | python3 -", want: []string{"wget -qO- https://x.io/get.py | python3"}},
	{name: "curl query string pipe", cmd: `curl -fsSL "https://x.io/i?a=1&b=2" | bash`, want: []string{"curl -fsSL https://x.io/i?a=1&b=2 | bash"}},
	{name: "curl escaped ampersand pipe", cmd: `curl https://x.io/i?a=1\&b=2 | sh`, want: []string{`curl https://x.io/i?a=1\&b=2 | sh`}},
	{name: "curl pipe env bash", cmd: "wget -qO- https://x.io/i | /usr/bin/env bash\ncurl https://x.io/j | env -i sh", want: []string{"wget -qO- https://x.io/i | /usr/bin/env bash", "curl https://x.io/j | env -i sh"}},
	{name: "global options before the subcommand", cmd: "apk --no-cache add curl; apt-get -qq install -y jq; apt-get -o Dpkg::Use-Pty=0 install git", want: []string{"curl", "jq", "git"}},
	{name: "pip global options", cmd: "pip --isolated install requests; python -m pip --disable-pip-version-check install flask; pip --cache-dir /c install a==1.0", want: []string{"requests", "flask"}},
	{name: "npm docker git global options", cmd: "npm --prefix web install left-pad; docker --context ci run redis:7; git -C deps clone https://g/x; git -c a=b clone https://g/z", want: []string{"left-pad", "redis:7", "https://g/x", "https://g/z"}},
	{name: "global option without a subcommand", cmd: "apk --version; git --no-pager", want: nil},
	{name: "git clone commit elsewhere on the line", cmd: "git clone https://g/x; echo " + sha, want: []string{"https://g/x"}},
	{name: "two clones one checkout", cmd: "git clone https://g/a && git clone https://g/b && git -C b checkout " + sha + "", want: []string{"https://g/a", "https://g/b"}},
	{name: "git pull", cmd: "git -C x pull --ff-only; git fetch", want: []string{"git pull"}},
	{name: "rpm packages", cmd: "dnf -y install curl curl-7.76.1-26.el9 java-17-openjdk && yum install ./x.rpm && microdnf install jq && zypper -n in git vim=9.0 && dnf update", want: []string{"curl", "java-17-openjdk", "jq", "git"}},
	{name: "releases latest", cmd: "curl -fsSL https://github.com/a/b/releases/latest/download/b.tgz | tar xz; wget -qO- https://github.com/a/b/releases/download/v1.0/b.tgz | tar xz", want: []string{"curl -fsSL https://github.com/a/b/releases/latest/download/b.tgz"}},
	{name: "python -c curl", cmd: `python3 -c "$(curl -fsSL https://x.io/i.py)"`, want: []string{"python3 -c $(curl"}},
	{name: "kubectl image", cmd: "kubectl run t --image=busybox:1.36 --rm; kubectl create deployment d --image nginx; kubectl run p --image=busybox" + dg + "; kubectl get pods", want: []string{"busybox:1.36", "nginx"}},
	{name: "helm chart version", cmd: "helm install r bitnami/redis; helm upgrade --install r oci://r.io/c --version 1.2.3; helm install ./charts/app --generate-name; helm install r2 c --repo https://x --version ^1; helm install r3 charts/local; helm upgrade r4 a.tgz --version=v2.0.0; helm list; helm install", scope: []string{"charts/local/Chart.yaml"}, want: []string{"bitnami/redis", "./charts/app", "c"}},
	{name: "git clone then fetch a commit", cmd: "git clone https://g/x && git -C x fetch --depth 1 origin " + sha + " && git -C x reset --hard " + sha, want: nil},
}

func TestShellCommandCases(t *testing.T) {
	for _, tc := range commandCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cmdRefs(tc.cmd, tc.scope...); !slices.Equal(got, tc.want) {
				t.Fatalf("refs = %q, want %q", got, tc.want)
			}
		})
	}
}

func write(t *testing.T, dir, name, data string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScan(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "z/compose.yaml", "services:\n  a:\n    image: b:1\n  c:\n    image: d:2\n")
	write(t, dir, "Dockerfile", "FROM a:1\nFROM a:1\n")
	write(t, dir, "main.go", "package main\n")
	write(t, dir, "big.sh", strings.Repeat("#", MaxFileSize+1))
	write(t, dir, "pnpm-lock.yaml", strings.Repeat("#", MaxFileSize+1)) // hashed, not scanned
	write(t, dir, "app.min.js", strings.Repeat("#", MaxFileSize+1))     // a large source file is skipped
	got := Scan(os.DirFS(dir), []string{"Dockerfile", "app.min.js", "big.sh", "gone.sh", "gone.py", "main.go", "pnpm-lock.yaml", "z/compose.yaml"}, []string{"docker", "run", "e:3"})
	var lines []string
	for _, f := range got {
		lines = append(lines, f.String())
	}
	want := []string{
		"Dockerfile:1 (container) a:1: a tag can be repushed: pin the image by digest (name:tag@sha256:...)",
		"Dockerfile:2 (container) a:1: a tag can be repushed: pin the image by digest (name:tag@sha256:...)",
		"big.sh (scan) big.sh: larger than 1 MiB: Aval cannot check its references; shrink or split the file",
		"gone.sh (scan) gone.sh: open gone.sh: no such file or directory: Aval cannot check its references; shrink or split the file",
		"runner.command:1 (container) e:3: a tag can be repushed: pin the image by digest (name:tag@sha256:...)",
		"z/compose.yaml:3 (container) b:1: a tag can be repushed: pin the image by digest (name:tag@sha256:...)",
		"z/compose.yaml:5 (container) d:2: a tag can be repushed: pin the image by digest (name:tag@sha256:...)",
	}
	if !slices.Equal(lines, want) {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	allowed := Finding{File: "a", Ecosystem: "go", Ref: "r", Fix: "f", Allowed: "why"}.String()
	if allowed != "a (go) r: f (allowed: why)" {
		t.Fatal(allowed)
	}
}

// TestScanKustomizeJSON: a .json file a kustomization lists in resources
// is a manifest and its images are checked; other .json files are not.
func TestScanKustomizeJSON(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "k8s/kustomization.yaml", "resources: [deploy.json, sub]\n---\nresources: x\n")
	write(t, dir, "k8s/sub/kustomization.yml", "resources:\n- ../svc.json\n- {a: b}\n")
	write(t, dir, "k8s/deploy.json", `{"spec": {"image": "evil:1"}}`)
	write(t, dir, "k8s/svc.json", `{"spec": {"image": "evil:2"}}`)
	write(t, dir, "k8s/other.json", `{"spec": {"image": "evil:3"}}`)
	files := []string{"k8s/deploy.json", "k8s/kustomization.yaml", "k8s/other.json", "k8s/sub/kustomization.yml", "k8s/svc.json"}
	var refs []string
	for _, f := range Scan(os.DirFS(dir), files, nil) {
		refs = append(refs, f.File+" "+f.Ref)
	}
	want := []string{"k8s/deploy.json evil:1", "k8s/kustomization.yaml k8s/kustomization.yaml", "k8s/sub/kustomization.yml resources[1]", "k8s/svc.json evil:2"}
	if !slices.Equal(refs, want) {
		t.Fatalf("refs = %q, want %q", refs, want)
	}
}

func TestScanDedupesAndSkipsUnknownFiles(t *testing.T) {
	dir := t.TempDir()
	if got := Scan(os.DirFS(dir), []string{"README.md"}, []string{"docker", "run", "a:1", "&&", "docker", "run", "a:1", "&&", "docker", "compose", "up"}); len(got) != 2 {
		t.Fatalf("got %v, want the image once and the compose file", got)
	}
}

// TestScanShebangScripts: an in-scope file without an extension is scanned
// when it starts with a shell shebang; other extension-less files are not.
func TestScanShebangScripts(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "setup", "#!/bin/sh\ncurl https://x.io/i | sh\n")
	write(t, dir, "bin/ci", "#!/usr/bin/env -S bash\napk --no-cache add curl\n")
	write(t, dir, "LICENSE", "curl https://x.io/i | sh\n")
	write(t, dir, "run", "#!/usr/bin/python3\nimport os\n")
	var refs []string
	for _, f := range Scan(os.DirFS(dir), []string{"LICENSE", "bin/ci", "run", "setup", "gone"}, nil) {
		refs = append(refs, f.File+" "+f.Ref)
	}
	want := []string{"bin/ci curl", "gone gone", "setup curl https://x.io/i | sh"}
	if !slices.Equal(refs, want) {
		t.Fatalf("refs = %q, want %q", refs, want)
	}
}

func TestExpandIsBounded(t *testing.T) {
	v := &vars{m: map[string]string{"A": strings.Repeat("x", MaxFileSize)}, budget: maxExpand}
	if got := v.expand("$A $A"); len(got) != 2*MaxFileSize+1 {
		t.Fatalf("expanded to %d bytes", len(got))
	}
	if got := v.expand("$A $A"); got != "$A $A" {
		t.Fatalf("expanded past the budget to %d bytes", len(got))
	}
	v = &vars{m: map[string]string{"A": "${B}", "B": "b"}, budget: maxExpand}
	if got := v.expand("$(A) ${B} $$C"); got != "b b $C" {
		t.Fatal(got)
	}
}

func TestNpmLineLookupsAreBounded(t *testing.T) {
	var deps []string
	for i := range maxLineLookups + 1 {
		deps = append(deps, fmt.Sprintf("%q: \"^1\"", fmt.Sprintf("d%03d", i)))
	}
	data := `{"dependencies": {` + strings.Join(deps, ",\n") + `}}`
	fs := npm{}.Detect(Input{Path: "package.json", Data: []byte(data), Has: func(string) bool { return false }})
	if len(fs) != maxLineLookups+1 || fs[0].Line != 1 || fs[maxLineLookups].Line != 0 {
		t.Fatalf("%d findings, first line %d, last line %d", len(fs), fs[0].Line, fs[len(fs)-1].Line)
	}
}

func TestLogicalLines(t *testing.T) {
	got := logicalLines("a \\\r\nb\r\nc\\\n")
	want := []line{{1, "a  b"}, {3, "c "}}
	if !slices.Equal(got, want) {
		t.Fatalf("%v", got)
	}
	if got := logicalLines("a\nb \\"); !slices.Equal(got, []line{{1, "a"}, {2, "b  "}}) {
		t.Fatalf("a continuation at the end is dropped: %v", got)
	}
	if lineOf([]byte("a\nb"), "zz") != 0 || lineOf([]byte("a\nb"), "b") != 2 {
		t.Fatal("lineOf")
	}
}

// FuzzDetect: no detector panics on untrusted content, and every finding
// points into the file it was given.
func FuzzDetect(f *testing.F) {
	for _, tc := range fileCases {
		f.Add(tc.path, tc.data)
	}
	for _, tc := range commandCases {
		f.Add("x.sh", tc.cmd)
	}
	f.Fuzz(func(t *testing.T, name, data string) {
		n := strings.Count(data, "\n") + 1
		for _, d := range Detectors {
			for _, path := range []string{name, "Dockerfile", "compose.yaml", "package.json", "requirements.txt", "pom.xml", "build.gradle", "x.sh", "go.mod", "Cargo.toml", ".gitmodules", "kustomization.yaml", "compose.json", "x_test.go"} {
				if !d.Applies(path) {
					continue
				}
				for _, fd := range d.Detect(Input{Path: path, Data: []byte(data), Has: func(string) bool { return false }}) {
					if fd.File != path || fd.Line < 0 || fd.Line > n || fd.Ref == "" || fd.Fix == "" {
						t.Fatalf("bad finding %+v for %d lines", fd, n)
					}
				}
			}
		}
	})
}
