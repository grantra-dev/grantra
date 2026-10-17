import { describe, expect, it } from "vitest";
import { cleanColumns, normalizeColumn, parseHeader, readHeader } from "./csv";

describe("CSV headers", () => {
  it("normalizes column names like the server", () => {
    expect(normalizeColumn("Employee ID")).toBe("employee_id");
    expect(normalizeColumn(" employee_id ")).toBe("employee_id");
    expect(normalizeColumn("Año de ingreso")).toBe("año_de_ingreso");
    expect(normalizeColumn("Cost-Center (EU)")).toBe("cost_center_eu");
    expect(normalizeColumn("!!!")).toBe("");
  });

  it("reads the first record, quotes and all", () => {
    expect(
      parseHeader('\uFEFFWorker ID, "Name, full" ,"Say ""hi""",,Dept\r\nW1,Ana,x,,Eng\n'),
    ).toEqual(["Worker ID", "Name, full", 'Say "hi"', "Dept"]);
    expect(parseHeader('"Multi\nline",b\n')).toEqual(["Multi\nline", "b"]);
    expect(parseHeader("")).toEqual([]);
  });

  it("drops empty mappings", () => {
    expect(cleanColumns({ employee_id: "Worker", email: " ", title: "" })).toEqual({
      employee_id: "Worker",
    });
  });

  it("reads a file", async () => {
    const file = new File(["Login,Roles\nana,Admin\n"], "erp.csv");
    expect(await readHeader(file)).toEqual(["Login", "Roles"]);
  });
});
