FROM brunoanjos/nova-server-base:latest

ENV executable="executable"
COPY $executable .
COPY configs.json .
COPY gymConfigs gymConfigs/
COPY pokemons.json .

CMD ["sh", "-c", "./$executable"]