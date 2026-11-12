---
"aval": minor
---

`aval gen dotnet`: the .NET adapter of `aval gen`. One unit per project (`.csproj`, `.fsproj`, `.vbproj`) from the solution at the contract root, or every project file without one, read from the solution and project files without the .NET SDK. `ProjectReference`s become dependencies; imported files, `Directory.Build.*` and `Directory.Packages.props` files above a project and items included from outside its directory join its scope; the test is `dotnet test <project>` (docs: Guides, Generate scope from the dependency graph).
