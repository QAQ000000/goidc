#!/usr/bin/env node

import { readFile, writeFile } from "node:fs/promises";

const inventoryPath = "docs/generated/legacy-inventory.json";
const serverPath = "internal/httpapi/server.go";
const outputPath = "docs/generated/route-coverage.json";

const inventory = JSON.parse(await readFile(inventoryPath, "utf8"));
const server = await readFile(serverPath, "utf8");

function normalizePath(path) {
  let value = path.trim().replace(/^\/+|\/+$/g, "");
  value = value.replace(/^api\/v1\/?/, "");
  value = value.replace(/\[?:([A-Za-z0-9_]+)\]?/g, ":$1");
  value = value.replace(/\{([A-Za-z0-9_]+)\}/g, ":$1");
  return value;
}

function legacyPath(expression) {
  const match = expression.match(/"([^"\n]+)"/);
  return match?.[1] ?? null;
}

const goRoutes = [...server.matchAll(/mux\.HandleFunc\("([A-Z]+) ([^"\n]+)"/g)].map((match) => {
  const method = match[1];
  const path = match[2];
  return { method, path, normalizedPath: normalizePath(path) };
});

const uniqueGoRoutes = [...new Map(goRoutes.map((route) => [`${route.method} ${route.path}`, route])).values()];
const byMethodAndPath = new Map(
  uniqueGoRoutes.map((route) => [`${route.method} ${route.normalizedPath}`, route]),
);

const routes = inventory.routes.map((route) => {
  const path = legacyPath(route.expression);
  const normalizedPath = path ? normalizePath(path) : null;
  const candidate = normalizedPath ? byMethodAndPath.get(`${route.method} ${normalizedPath}`) : undefined;
  return {
    legacy: {
      file: route.file,
      line: route.line,
      method: route.method,
      path,
      controller: route.controller,
      expression: route.expression,
    },
    review_state: "unreviewed",
    candidate_go_routes: candidate ? [{ method: candidate.method, path: candidate.path }] : [],
    evidence_required: ["handler behavior", "authentication and permission", "state transitions", "page/API contract"],
  };
});

const exactCandidateCount = routes.filter((route) => route.candidate_go_routes.length > 0).length;
const result = {
  generated_at: new Date().toISOString(),
  source: {
    legacy_inventory: inventoryPath,
    legacy_generated_at: inventory.generatedAt,
    go_router: serverPath,
  },
  disclaimer: "This is an evidence index, not a compatibility claim. A candidate is only an exact method/path match; every row remains unreviewed until behavior, permissions, state, and page/API contracts are verified.",
  summary: {
    legacy_route_calls: routes.length,
    current_go_route_registrations: goRoutes.length,
    current_go_unique_routes: uniqueGoRoutes.length,
    exact_method_path_candidates: exactCandidateCount,
    unreviewed_routes: routes.length,
  },
  routes,
};

await writeFile(outputPath, `${JSON.stringify(result, null, 2)}\n`);
console.log(JSON.stringify(result.summary));
