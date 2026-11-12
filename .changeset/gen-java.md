---
"aval": minor
---

`aval gen java`: the Java and Kotlin adapter. One unit per Maven module or Gradle subproject, read from `pom.xml`, `settings.gradle(.kts)` and `build.gradle(.kts)` without running a JVM tool; only references to modules of the same build become edges, and anything it cannot read for certain (computed paths, `includeBuild`, sources outside `src`) is an error (docs: Guides, Generate scope from the dependency graph).
