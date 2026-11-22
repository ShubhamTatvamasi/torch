# Example plan

Import with:

```sh
torch init --goal "Ship a small, tested items service" --from PLAN.example.md
```

Steps are `- [ ]` checkboxes. Indented bullets under a step become its
acceptance criteria. Headings are prefixed to the step title.

# Foundation

- [ ] Create the database schema
  - a migration creates an `items` table
  - migrations run cleanly from an empty database
  - the schema is committed

# API

- [ ] Add the items API
  - `GET /items` returns 200 with a JSON array
  - `POST /items` validates input and returns 201
  - both endpoints are covered by tests

- [ ] Add error handling
  - invalid input returns 422 with a useful message
  - a missing item returns 404
  - tests cover both paths

# Finish

- [ ] Write the README
  - setup, run, and test instructions are accurate
  - a new contributor can start from a clean clone

- [ ] Run the full verification once
  - the build passes from a clean checkout
  - the test suite passes
  - the commands in the README work as written
