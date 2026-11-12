rootProject.name = "shop"

include(":common", ":inventory")
include(
    ":services:api",
    ":services:web",
)
// include(":ghost") is a comment, not a project.
