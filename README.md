# Pau FC Analyse

Application d'analyse de match de la cellule analyse du Pau FC : dashboard Collectif / Indiv, codage live (fenêtres collective et indiv), xG, rapport PDF.

- `app/` : page de l'application et numéro de version. L'application de bureau la télécharge au lancement pour se mettre à jour toute seule.
- `source/` : programme de bureau (Go) qui sert la page et stocke les données sur l'ordinateur.

Aucune donnée de match n'est stockée ici : matchs, documents et clé API restent sur l'ordinateur de l'utilisateur.
