// Cascading location selects.
//
// The UI walks district > subcounty > parish > village and skips county: the
// tier is mandatory in the data — subcounty codes are unique only within a
// county — but nobody selects it, so it is derived from the path server-side.
//
// Each dependent select declares the select it hangs off and the level it
// fetches, so the chain is described in the HTML rather than repeated here.
(function () {
  "use strict";

  // Any form that declares the cascade gets it: the CHW form, where the
  // placement is being chosen, and the register's filter bar, where a subtree
  // is being narrowed to.
  Array.prototype.forEach.call(
    document.querySelectorAll("[data-location-cascade]"), cascade);

function cascade(form) {

  // steps[0] is the district select, which has no parent; the rest each fetch
  // their level from the one before.
  var steps = [document.getElementById("district_id")].concat(
    Array.prototype.slice.call(form.querySelectorAll("select[data-under]"))
  );
  if (steps.some(function (step) { return !step; })) return;

  // The placement to rebuild on edit, captured from the server's markup before
  // anything clears it. Held here rather than left on the elements: resetting a
  // select has to forget its pending selection, and reading both from the same
  // dataset attribute makes those two jobs collide.
  var wanted = steps.map(function (step) { return step.dataset.selected || ""; });

  function placeholder(select, text) {
    select.innerHTML = "";
    var option = document.createElement("option");
    option.value = "";
    option.textContent = text;
    select.appendChild(option);
  }

  function reset(index) {
    for (var i = index; i < steps.length; i++) {
      wanted[i] = "";
      placeholder(steps[i], "— choose a " + steps[i - 1].dataset.level + " first —");
      steps[i].disabled = true;
    }
  }

  // fill loads step `index` from the value of the step before it, then follows
  // any prefilled selection further down the chain.
  function fill(index) {
    var select = steps[index];
    var under = steps[index - 1].value;
    if (!under) { reset(index); return; }

    select.disabled = true;
    placeholder(select, "loading…");

    fetch("/api/locations?level=" + encodeURIComponent(select.dataset.level) +
          "&under=" + encodeURIComponent(under), { credentials: "same-origin" })
      .then(function (response) {
        if (!response.ok) throw new Error("lookup failed");
        return response.json();
      })
      .then(function (places) {
        placeholder(select, places.length ? "— choose —" : "— none found —");
        places.forEach(function (place) {
          var option = document.createElement("option");
          option.value = place.id;
          option.textContent = place.name;
          select.appendChild(option);
        });
        select.disabled = false;
        applyCadre();

        // On edit the server sends the existing placement down, so the chain
        // rebuilds itself instead of the browser guessing.
        var pending = wanted[index];
        wanted[index] = "";
        if (pending && pending !== "0") {
          select.value = pending;
          if (select.value && index + 1 < steps.length) fill(index + 1);
        }
      })
      .catch(function () {
        placeholder(select, "— could not load, try again —");
        select.disabled = false;
      });
  }

  steps.forEach(function (select, index) {
    if (index + 1 >= steps.length) return;
    select.addEventListener("change", function () {
      reset(index + 1);
      fill(index + 1);
    });
  });

  // Cadre decides how deep the placement goes: a CHEW is placed at parish
  // level, a VHT at village level. Hiding the village select is not enough —
  // its value would still post — so it is cleared and disabled with it.
  var villageField = form.querySelector(".village-field");
  var village = document.getElementById("village_id");

  function applyCadre() {
    var checked = form.querySelector("input[name=cadre]:checked");
    var wantsVillage = !checked || checked.dataset.level === "village";
    if (villageField) villageField.hidden = !wantsVillage;
    if (!village) return;
    if (wantsVillage) {
      village.disabled = village.options.length <= 1;
    } else {
      village.value = "";
      village.disabled = true;
    }
  }

  Array.prototype.forEach.call(form.querySelectorAll("input[name=cadre]"), function (radio) {
    radio.addEventListener("change", applyCadre);
  });

  // Disable the dependent selects without calling reset(): reset forgets the
  // pending placement, which is exactly what the edit form needs kept.
  for (var i = 1; i < steps.length; i++) steps[i].disabled = true;
  applyCadre();
  if (steps[0].value) fill(1);
}
})();
